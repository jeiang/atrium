package proposals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/writes"
)

const (
	OutcomeUndone     = "undone"
	OutcomeNotUndone  = "not-undone"
	OutcomeApplied    = "applied"
	OutcomeNotApplied = "not-applied"
	OutcomePartial    = "partial"
	OutcomeSettled    = "settled"

	ResolveProposalAction = "proposal_action"
	ResolveAutoUndo       = "auto_undo"
	ResolveAutoAction     = "auto_action"

	SettledText    = "settled by operator after checking the mailbox"
	NotAppliedText = "the operator checked: the write did not happen"

	UnknownOutcomeText = "Outcome unknown"
)

var Outcomes = []string{OutcomeApplied, OutcomeNotApplied, OutcomePartial, OutcomeUndone, OutcomeNotUndone}

type Resolver struct {
	Actor  string
	Source string
}

func RecordResolution(ctx context.Context, x Execer, who Resolver, ts float64, kind string, rowID int64, proposalID *int64, outcome, prior string) error {
	if prior == "" {
		prior = UnknownOutcomeText
	}
	return writes.RecordResolution(ctx, x, who.Actor, who.Source, ts, kind, rowID, proposalID, outcome, prior)
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
	legacy, err := p.resolveLegacy(ctx, id, outcome, who)
	if err != nil {
		return out, err
	}
	out.Settled = append(out.Settled, legacy...)
	if len(out.Settled) == 0 {
		return out, ProposalError(fmt.Sprintf("proposal %d has no blocked write", id))
	}
	return out, nil
}

func (p *Pipeline) resolveLegacy(ctx context.Context, id int64, outcome string, who Resolver) ([]int64, error) {
	settled := []int64{}
	err := p.DB.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id, lease, kind, undo_of, state, error FROM proposal_actions "+
			"WHERE proposal_id = ? AND state IN ('held', 'unknown', 'partial', 'undo_held', 'undo_unknown', 'undo_partial') ORDER BY id", id)
		if err != nil {
			return err
		}
		type held struct {
			id     int64
			lease  float64
			kind   string
			undoOf *int64
			state  ledger.State
			error  sql.NullString
		}
		blocked, err := db.Collect(rows, func(r *sql.Rows) (held, error) {
			var h held
			return h, r.Scan(&h.id, &h.lease, &h.kind, &h.undoOf, &h.state, &h.error)
		})
		if err != nil {
			return err
		}
		if len(blocked) == 0 {
			return nil
		}
		now := p.now()
		for _, h := range blocked {
			isUndo := h.kind == "undo"
			switch {
			case (h.state == ledger.Held || h.state == ledger.UndoHeld) && now-h.lease < StuckSeconds:
				return ProposalError(fmt.Sprintf("action %d may still be running; wait until atrium is idle and try again", h.id))
			case isUndo && outcome == "":
				return ProposalError(fmt.Sprintf("action %d is a blocked undo; check the mailbox and say whether the message is %s or %s", h.id, OutcomeUndone, OutcomeNotUndone))
			}
		}
		for _, h := range blocked {
			recorded := OutcomeSettled
			state := ledger.Settled
			if h.kind == "undo" {
				recorded = outcome
				if outcome == OutcomeUndone {
					state = ledger.OK
				}
			}
			if _, err := tx.ExecContext(ctx, "UPDATE proposal_actions SET state = ?, error = CASE WHEN ? = 'ok' THEN NULL ELSE ? END WHERE id = ?",
				state, state, SettledText, h.id); err != nil {
				return err
			}
			if err := RecordResolution(ctx, tx, who, now, ResolveProposalAction, h.id, &id, recorded, h.error.String); err != nil {
				return err
			}
			settled = append(settled, h.id)
		}
		return nil
	})
	return settled, err
}
