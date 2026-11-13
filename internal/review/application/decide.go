package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
)

var log = logx.Logger("atrium.review")

type ApproveInput struct {
	ProposalID     int64
	Selection      any
	SelectionValue any
}

type DismissInput struct {
	ProposalID int64
	Suppress   bool
}

type Decision struct {
	Proposal *proposals.Proposal
	Applied  bool
}

type PhoneInput struct {
	Token string
	Now   float64
}

func WithOrigin(ctx context.Context, actor, source string) (context.Context, error) {
	if actor == "" {
		return ctx, ledger.Refused("a resolution needs an actor")
	}
	if !audit.Source(source).Valid() {
		return ctx, invalid("source", "Unknown resolution source.")
	}
	return audit.With(ctx, actor, audit.Source(source)), nil
}

func resolver(ctx context.Context) (proposals.Resolver, error) {
	if !audit.HasActor(ctx) {
		return proposals.Resolver{}, audit.ErrNoActor
	}
	return proposals.Resolver{Actor: audit.Actor(ctx), Source: string(audit.SourceOf(ctx))}, nil
}

func (s *Service) Approve(ctx context.Context, in ApproveInput) (application.Change[Decision], error) {
	who, err := resolver(ctx)
	if err != nil {
		return application.Change[Decision]{}, err
	}
	if err := s.known(ctx, in.ProposalID); err != nil {
		return application.Change[Decision]{}, s.refusal(ctx, ApproveName, proposalTarget(in.ProposalID), err, nil)
	}
	if in.SelectionValue != nil {
		chosen, ok := in.SelectionValue.(map[string]any)
		if !ok {
			return application.Change[Decision]{}, s.refusal(ctx, ApproveName, proposalTarget(in.ProposalID), invalid("selection", "selection must be an object."), nil)
		}
		in.Selection = chosen
	}
	return s.approve(ctx, in, who, nil)
}

func (s *Service) approve(ctx context.Context, in ApproveInput, who proposals.Resolver, check func(context.Context, db.Querier) error) (application.Change[Decision], error) {
	done, err := s.pipeline.ApproveAndApply(ctx, in.ProposalID, in.Selection, who, check)
	target := proposalTarget(in.ProposalID)
	var badSelection proposals.SelectionError
	if errors.As(err, &badSelection) {
		return application.Change[Decision]{}, s.refusal(ctx, ApproveName, target, invalid("selection", err.Error()), nil)
	}
	out := application.Change[Decision]{Data: Decision{Proposal: done}}
	if done != nil {
		out.Data.Applied = done.Status == proposals.Applied
		out.EffectIDs = s.proposalEffects(ctx, in.ProposalID)
	}
	if err != nil {
		if refused := pipelineRefusal(err); refused != nil {
			refused.EffectIDs = out.EffectIDs
			if done != nil {
				out.AuditID = s.audit(ctx, ApproveName, target, state.Partial, out.EffectIDs)
				return out, refused
			}
			return out, s.refusal(ctx, ApproveName, target, refused, nil)
		}
		return out, s.refusal(ctx, ApproveName, target, err, out.EffectIDs)
	}
	outcome := state.OK
	if !out.Data.Applied {
		outcome = state.Failed
	}
	out.AuditID = s.audit(ctx, ApproveName, target, outcome, out.EffectIDs)
	return out, nil
}

func (s *Service) Dismiss(ctx context.Context, in DismissInput) (application.Change[Decision], error) {
	who, err := resolver(ctx)
	if err != nil {
		return application.Change[Decision]{}, err
	}
	if err := s.known(ctx, in.ProposalID); err != nil {
		return application.Change[Decision]{}, s.refusal(ctx, DismissName, proposalTarget(in.ProposalID), err, nil)
	}
	return s.dismiss(ctx, in, who)
}

func (s *Service) dismiss(ctx context.Context, in DismissInput, who proposals.Resolver) (application.Change[Decision], error) {
	status := proposals.Dismissed
	if in.Suppress {
		status = proposals.Suppressed
	}
	target := proposalTarget(in.ProposalID)
	if err := s.pipeline.Dismiss(ctx, in.ProposalID, status, who); err != nil {
		if refused := pipelineRefusal(err); refused != nil {
			return application.Change[Decision]{}, s.refusal(ctx, DismissName, target, refused, nil)
		}
		return application.Change[Decision]{}, s.refusal(ctx, DismissName, target, err, nil)
	}
	done, err := proposals.Get(ctx, s.db.Read, in.ProposalID)
	if err != nil {
		return application.Change[Decision]{}, s.refusal(ctx, DismissName, target, err, nil)
	}
	return application.Change[Decision]{Data: Decision{Proposal: done}, AuditID: s.audit(ctx, DismissName, target, state.OK, nil)}, nil
}

func (s *Service) Phone(ctx context.Context, in PhoneInput) (application.Change[Decision], error) {
	ctx = audit.With(ctx, "phone", audit.Phone)
	who := proposals.Resolver{Actor: "phone", Source: string(audit.Phone)}
	claims, err := actionlink.Verify(ctx, s.db.Write, in.Token, in.Now)
	if err != nil {
		return application.Change[Decision]{}, err
	}
	name := DismissName
	if claims.Action == actionlink.Approve {
		name = ApproveName
	}
	pol, err := policy.Load(ctx, s.db.Read)
	if err != nil {
		s.audit(ctx, name, proposalTarget(claims.ProposalID), state.Failed, nil)
		return application.Change[Decision]{}, fmt.Errorf("%w: %w", actionlink.ErrUnavailable, err)
	}
	entered := false
	delegated := false
	var out application.Change[Decision]
	err = actionlink.Redeem(ctx, s.db.Write, in.Token, in.Now, func(ctx context.Context, claims actionlink.Claims) error {
		entered = true
		prop, err := proposals.Get(ctx, s.db.Write, claims.ProposalID)
		if err != nil {
			return err
		}
		if prop == nil || prop.Fingerprint != claims.Hash {
			return actionlink.ErrInvalid
		}
		if prop.Kind == permission.Kind || !pol.Allows(prop.Kind) {
			return actionlink.ErrRefused
		}
		delegated = true
		if claims.Action == actionlink.Approve {
			out, err = s.approve(ctx, ApproveInput{ProposalID: prop.ID}, who, func(ctx context.Context, q db.Querier) error {
				return actionlink.CheckRedemption(ctx, q, claims.Nonce, in.Now)
			})
			return err
		}
		out, err = s.dismiss(ctx, DismissInput{ProposalID: prop.ID}, who)
		return err
	})
	if entered && !delegated && err != nil {
		outcome := state.Failed
		if errors.Is(err, actionlink.ErrInvalid) {
			outcome = state.Invalid
		}
		if errors.Is(err, actionlink.ErrRefused) {
			outcome = state.Refused
		}
		out.AuditID = s.audit(ctx, name, proposalTarget(claims.ProposalID), outcome, nil)
	}
	if delegated && out.Data.Proposal == nil && errors.As(err, new(*db.LockBusy)) {
		if rerr := actionlink.Release(context.WithoutCancel(ctx), s.db.Write, claims.Nonce, in.Now); rerr != nil {
			log.Error("restoring a phone link after a busy lock failed", "proposal_id", claims.ProposalID, logx.Err, rerr.Error())
		}
	}
	return out, err
}

func (s *Service) known(ctx context.Context, id int64) error {
	found, err := proposals.Get(ctx, s.db.Read, id)
	if err != nil {
		return err
	}
	if found == nil {
		return notFound("Unknown approval.")
	}
	return nil
}

func (s *Service) proposalEffects(ctx context.Context, id int64) []int64 {
	if s.ledger == nil {
		return nil
	}
	found, err := s.ledger.List(ctx, ledger.Filter{ProposalID: &id})
	if err != nil {
		log.Error("listing the effects of a decision failed", "proposal_id", id, logx.Err, err.Error())
		return nil
	}
	out := make([]int64, len(found))
	for i, e := range found {
		out[i] = e.ID
	}
	return out
}

func pipelineRefusal(err error) *application.Error {
	if errors.Is(err, actionlink.ErrUsed) {
		return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
	}
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Unavailable, Message: err.Error() + "; try again shortly", Cause: err}
	}
	var refused proposals.ProposalError
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		return &application.Error{Code: application.Refused, Message: err.Error()}
	}
	return nil
}

func ledgerRefusal(err error) *application.Error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Unavailable, Message: err.Error() + "; try again shortly"}
	}
	var refused ledger.Refused
	var conflict *ledger.Conflict
	var settled *ledger.SettledMeanwhile
	var disabled *module.OwnerDisabled
	var missing *ledger.NotFound
	switch {
	case errors.As(err, &missing):
		return &application.Error{Code: application.NotFound, Message: "Unknown effect."}
	case errors.As(err, &conflict) || errors.As(err, &settled):
		return &application.Error{Code: application.Conflict, Message: err.Error()}
	case errors.As(err, &refused) || errors.As(err, &disabled):
		return &application.Error{Code: application.Refused, Message: err.Error()}
	}
	return nil
}

func (s *Service) refusal(ctx context.Context, name application.Name, target string, err error, effects []int64) error {
	var e *application.Error
	if !errors.As(err, &e) {
		s.audit(ctx, name, target, state.Failed, effects)
		return err
	}
	outcome := state.Refused
	switch e.Code {
	case application.Invalid:
		outcome = state.Invalid
	case application.NotFound:
		outcome = state.NotFound
	case application.Conflict:
		outcome = state.Conflict
	}
	s.audit(ctx, name, target, outcome, effects)
	return err
}

func (s *Service) audit(ctx context.Context, name application.Name, target string, outcome state.Outcome, effects []int64) int64 {
	details := "{}"
	if len(effects) > 0 {
		raw, err := json.Marshal(map[string][]int64{"effect_ids": effects})
		if err == nil {
			details = string(raw)
		}
	}
	id, err := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: details})
	if err != nil {
		log.Error("operation audit failed", "operation", string(name), logx.Err, err.Error())
	}
	return id
}

func proposalTarget(id int64) string { return "proposal:" + strconv.FormatInt(id, 10) }

func effectTarget(id int64) string { return "effect:" + strconv.FormatInt(id, 10) }
