package proposals

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/ledger"
)

const (
	OutcomeUndone     = "undone"
	OutcomeNotUndone  = "not-undone"
	OutcomeApplied    = "applied"
	OutcomeNotApplied = "not-applied"
	OutcomePartial    = "partial"

	UnknownOutcomeText = "Outcome unknown"
)

var Outcomes = []string{OutcomeApplied, OutcomeNotApplied, OutcomePartial, OutcomeUndone, OutcomeNotUndone}

type Resolver struct {
	Actor  string
	Source string
}

const ErrSettledMeanwhile = ProposalError("the write was settled by the operator while it ran; the recorded outcome stands")

type Resolved struct {
	ID      int64
	Settled []int64
}

func refusal(err error) error {
	var refused ledger.Refused
	var settled *ledger.SettledMeanwhile
	var missing *ledger.NotFound
	if errors.As(err, &refused) || errors.As(err, &settled) || errors.As(err, &missing) {
		return ProposalError(err.Error())
	}
	return err
}

func (p *Pipeline) Resolve(ctx context.Context, id int64, outcome string, who Resolver) (Resolved, error) {
	if outcome != "" && !slices.Contains(Outcomes, outcome) {
		return Resolved{}, ProposalError("outcome must be one of applied, not-applied, partial, undone, not-undone")
	}
	ctx = context.WithoutCancel(ctx)
	out := Resolved{ID: id, Settled: []int64{}}
	prop, err := Get(ctx, p.DB.Write, id)
	if err != nil {
		return Resolved{}, err
	}
	if prop == nil {
		return Resolved{}, ProposalError(fmt.Sprintf("unknown proposal %d", id))
	}
	if prop.Status == Approved {
		return Resolved{}, ProposalError(fmt.Sprintf("proposal %d is still being applied", id))
	}
	effects, err := p.Ledger.ProposalEffects(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	var blocked []ledger.Effect
	for _, e := range effects {
		if e.Blocked {
			blocked = append(blocked, e)
		}
	}
	for _, e := range blocked {
		if e.State == ledger.Intent {
			return Resolved{}, ProposalError(fmt.Sprintf("effect %d may still be running; wait until atrium is idle and try again", e.ID))
		}
		if outcome == "" {
			if e.Direction == "undo" {
				return Resolved{}, ProposalError(fmt.Sprintf("effect %d is a blocked undo; check the mailbox and say whether it was %s or %s", e.ID, OutcomeUndone, OutcomeNotUndone))
			}
			return Resolved{}, ProposalError(fmt.Sprintf("effect %d is blocked; say whether it was %s, %s or %s", e.ID, OutcomeApplied, OutcomeNotApplied, OutcomePartial))
		}
		if _, err := p.Ledger.Resolve(ctx, e.ID, ledger.Resolution{Outcome: outcome},
			ledger.Resolver{Actor: who.Actor, Source: who.Source}, nil); err != nil {
			return out, refusal(err)
		}
		out.Settled = append(out.Settled, e.ID)
	}
	if len(out.Settled) == 0 {
		return out, ProposalError(fmt.Sprintf("proposal %d has no blocked write", id))
	}
	return out, nil
}
