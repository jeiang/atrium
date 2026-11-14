package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

type TargetKind string

const (
	ProposalTarget TargetKind = "proposal"
	EffectTarget   TargetKind = "effect"
	MailTarget     TargetKind = "mail"
)

type Target struct {
	Kind TargetKind
	ID   int64
}
type UndoInput struct{ Target Target }

type UndoResult struct {
	Proposal *proposals.Proposal
	Outcome  proposals.UndoResult
	Effect   *ledger.Effect
	Undo     *ledger.Effect
	Mail     *live.UndoResult
}

type ResolveInput struct {
	Target   Target
	Outcome  string
	Evidence json.RawMessage
}

type ResolveResult struct {
	Proposal *proposals.Resolved
	Effect   *ledger.Effect
	Mail     *live.Resolved
}

func targetOf(t Target) (string, error) {
	switch t.Kind {
	case ProposalTarget, EffectTarget, MailTarget:
		return string(t.Kind) + ":" + strconv.FormatInt(t.ID, 10), nil
	}
	return "", invalid("target", "Name one proposal, effect or mail action.")
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
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Unavailable, Message: err.Error() + "; try again shortly"}
	}
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
	target, err := targetOf(in.Target)
	if err != nil {
		return application.Change[UndoResult]{}, err
	}
	if in.Target.Kind == EffectTarget {
		return s.undoEffect(ctx, in.Target.ID, who, target)
	}
	if in.Target.Kind == MailTarget {
		if err := s.knownAction(ctx, in.Target.ID); err != nil {
			return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, nil)
		}
		done, err := s.live.Undo(ctx, in.Target.ID)
		if err != nil {
			if refused := mailRefusal(err); refused != nil {
				return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, refused, []int64{in.Target.ID})
			}
			return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, []int64{in.Target.ID})
		}
		return application.Change[UndoResult]{Data: UndoResult{Mail: &done}, EffectIDs: []int64{in.Target.ID, done.UndoEffectID}, AuditID: s.audit(ctx, UndoName, target, state.OK, []int64{in.Target.ID, done.UndoEffectID})}, nil
	}
	if err := s.known(ctx, in.Target.ID); err != nil {
		return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, nil)
	}
	outcome, err := s.pipeline.Undo(ctx, in.Target.ID, who)
	out := application.Change[UndoResult]{Data: UndoResult{Outcome: outcome}, EffectIDs: s.proposalEffects(ctx, in.Target.ID)}
	after, gerr := proposals.Get(ctx, s.db.Read, in.Target.ID)
	if gerr != nil {
		return out, s.refusal(ctx, UndoName, target, gerr, out.EffectIDs)
	}
	out.Data.Proposal = after
	if err != nil {
		refused := pipelineRefusal(err)
		if refused == nil {
			return out, s.refusal(ctx, UndoName, target, err, out.EffectIDs)
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
		return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, []int64{id})
	}
	forward, err := s.ledger.Get(ctx, id)
	if err != nil {
		return application.Change[UndoResult]{}, s.refusal(ctx, UndoName, target, err, []int64{id})
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
	target, err := targetOf(in.Target)
	if err != nil {
		return application.Change[ResolveResult]{}, err
	}
	if in.Target.Kind == EffectTarget {
		found, err := s.ledger.Resolve(ctx, in.Target.ID, ledger.Resolution{Outcome: in.Outcome, Evidence: in.Evidence}, ledger.Resolver{Actor: who.Actor, Source: who.Source}, nil)
		if err != nil {
			if refused := ledgerRefusal(err); refused != nil {
				return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, []int64{in.Target.ID})
			}
			return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, []int64{in.Target.ID})
		}
		effects := []int64{in.Target.ID}
		return application.Change[ResolveResult]{
			Data: ResolveResult{Effect: &found}, EffectIDs: effects, AuditID: s.audit(ctx, ResolveName, target, state.OK, effects),
		}, nil
	}
	if in.Target.Kind == MailTarget {
		if err := s.knownAction(ctx, in.Target.ID); err != nil {
			return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, []int64{in.Target.ID})
		}
		done, err := s.live.Resolve(ctx, in.Target.ID, in.Outcome, who)
		if err != nil {
			if refused := mailRefusal(err); refused != nil {
				return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, []int64{in.Target.ID})
			}
			return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, []int64{in.Target.ID})
		}
		return application.Change[ResolveResult]{Data: ResolveResult{Mail: &done}, EffectIDs: []int64{in.Target.ID}, AuditID: s.audit(ctx, ResolveName, target, state.OK, []int64{in.Target.ID})}, nil
	}
	if err := s.known(ctx, in.Target.ID); err != nil {
		return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, nil)
	}
	resolved, err := s.pipeline.Resolve(ctx, in.Target.ID, in.Outcome, who)
	if err != nil {
		if refused := pipelineRefusal(err); refused != nil {
			return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, refused, []int64{in.Target.ID})
		}
		return application.Change[ResolveResult]{}, s.refusal(ctx, ResolveName, target, err, nil)
	}
	return application.Change[ResolveResult]{
		Data: ResolveResult{Proposal: &resolved}, EffectIDs: resolved.Settled, AuditID: s.audit(ctx, ResolveName, target, state.OK, resolved.Settled),
	}, nil
}
