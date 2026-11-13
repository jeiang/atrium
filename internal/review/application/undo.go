package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

type UndoInput struct {
	ProposalID int64
	EffectID   int64
	ActionID   int64
}

type UndoResult struct {
	Proposal *proposals.Proposal
	Outcome  proposals.UndoResult
	Effect   *ledger.Effect
	Undo     *ledger.Effect
	Mail     *live.UndoResult
}

type ResolveInput struct {
	ProposalID int64
	EffectID   int64
	ActionID   int64
	Outcome    string
	Evidence   json.RawMessage
}

type ResolveResult struct {
	Proposal *proposals.Resolved
	Effect   *ledger.Effect
	Mail     *live.Resolved
}

func targetOf(proposal, effect, action int64) (string, error) {
	named := 0
	for _, id := range []int64{proposal, effect, action} {
		if id != 0 {
			named++
		}
	}
	switch {
	case named > 1:
		return "", invalid("target", "Name one proposal, effect or mail action.")
	case effect != 0:
		return effectTarget(effect), nil
	case action != 0:
		return "mail:" + strconv.FormatInt(action, 10), nil
	}
	return proposalTarget(proposal), nil
}

func (s *Service) knownAction(ctx context.Context, id int64) error {
	found, err := s.live.Get(ctx, id)
	if err != nil {
		return err
	}
	if found == nil {
		return notFound("Unknown action.")
	}
	return nil
}

func mailRefusal(err error) *application.Error {
	var refused live.UndoError
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		return &application.Error{Code: application.Refused, Message: err.Error()}
	}
	return nil
}

func (s *Service) Undo(ctx context.Context, in UndoInput) (application.Change[UndoResult], error) {
	who, err := resolver(ctx)
	if err != nil {
		return application.Change[UndoResult]{}, err
	}
	target, err := targetOf(in.ProposalID, in.EffectID, in.ActionID)
	if err != nil {
		return application.Change[UndoResult]{}, err
	}
	if in.EffectID != 0 {
		return s.undoEffect(ctx, in.EffectID, who, target)
	}
	if in.ActionID != 0 {
		if err := s.knownAction(ctx, in.ActionID); err != nil {
			return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, nil)
		}
		done, err := s.live.Undo(ctx, in.ActionID)
		if err != nil {
			if refused := mailRefusal(err); refused != nil {
				return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, refused, nil)
			}
			return application.Change[UndoResult]{}, err
		}
		return application.Change[UndoResult]{Data: UndoResult{Mail: &done}, AuditID: s.audit(ctx, UndoName, target, state.OK, nil)}, nil
	}
	if err := s.known(ctx, in.ProposalID); err != nil {
		return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, nil)
	}
	outcome, err := s.pipeline.Undo(ctx, in.ProposalID, who)
	out := application.Change[UndoResult]{Data: UndoResult{Outcome: outcome}, EffectIDs: s.proposalEffects(ctx, in.ProposalID)}
	after, gerr := proposals.Get(ctx, s.db.Read, in.ProposalID)
	if gerr != nil {
		return out, gerr
	}
	out.Data.Proposal = after
	if err != nil {
		refused := pipelineRefusal(err)
		if refused == nil {
			return out, err
		}
		refused.EffectIDs = out.EffectIDs
		if len(outcome.Outcomes) > 0 || outcome.SyncError != nil {
			out.AuditID = s.audit(ctx, UndoName, target, state.Partial, out.EffectIDs)
			return out, refused
		}
		return out, s.refusal(ctx, UndoName, target, refused, out.EffectIDs)
	}
	result := state.OK
	if !outcome.Undone {
		result = state.Failed
	}
	out.AuditID = s.audit(ctx, UndoName, target, result, out.EffectIDs)
	return out, nil
}

func (s *Service) undoEffect(ctx context.Context, id int64, who proposals.Resolver, target string) (application.Change[UndoResult], error) {
	undo, err := s.ledger.Undo(ctx, id, ledger.Resolver{Actor: who.Actor, Source: who.Source})
	if err != nil {
		if refused := ledgerRefusal(err); refused != nil {
			return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, refused, []int64{id})
		}
		return application.Change[UndoResult]{}, err
	}
	forward, err := s.ledger.Get(ctx, id)
	if err != nil {
		return application.Change[UndoResult]{}, err
	}
	effects := []int64{id, undo.ID}
	return application.Change[UndoResult]{
		Data: UndoResult{Effect: &forward, Undo: &undo}, EffectIDs: effects, AuditID: s.audit(ctx, UndoName, target, state.OK, effects),
	}, nil
}

func (s *Service) Resolve(ctx context.Context, in ResolveInput) (application.Change[ResolveResult], error) {
	who, err := resolver(ctx)
	if err != nil {
		return application.Change[ResolveResult]{}, err
	}
	target, err := targetOf(in.ProposalID, in.EffectID, in.ActionID)
	if err != nil {
		return application.Change[ResolveResult]{}, err
	}
	if in.EffectID != 0 {
		found, err := s.ledger.Resolve(ctx, in.EffectID, ledger.Resolution{Outcome: in.Outcome, Evidence: in.Evidence}, ledger.Resolver{Actor: who.Actor, Source: who.Source}, nil)
		if err != nil {
			if refused := ledgerRefusal(err); refused != nil {
				return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, []int64{in.EffectID})
			}
			return application.Change[ResolveResult]{}, err
		}
		effects := []int64{in.EffectID}
		return application.Change[ResolveResult]{
			Data: ResolveResult{Effect: &found}, EffectIDs: effects, AuditID: s.audit(ctx, ResolveName, target, state.OK, effects),
		}, nil
	}
	if in.ActionID != 0 {
		done, err := s.live.Resolve(ctx, in.ActionID, in.Outcome, who)
		if err != nil {
			if refused := mailRefusal(err); refused != nil {
				return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, nil)
			}
			return application.Change[ResolveResult]{}, err
		}
		return application.Change[ResolveResult]{Data: ResolveResult{Mail: &done}, AuditID: s.audit(ctx, ResolveName, target, state.OK, nil)}, nil
	}
	if err := s.known(ctx, in.ProposalID); err != nil {
		return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, nil)
	}
	resolved, err := s.pipeline.Resolve(ctx, in.ProposalID, in.Outcome, who)
	if err != nil {
		if refused := pipelineRefusal(err); refused != nil {
			return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, nil)
		}
		return application.Change[ResolveResult]{}, err
	}
	return application.Change[ResolveResult]{
		Data: ResolveResult{Proposal: &resolved}, EffectIDs: resolved.Settled, AuditID: s.audit(ctx, ResolveName, target, state.OK, resolved.Settled),
	}, nil
}
