package proposals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	OutcomeUndone    = "undone"
	OutcomeNotUndone = "not-undone"
	OutcomeSettled   = "settled"

	ResolveProposalAction = "proposal_action"
	ResolveAutoUndo       = "auto_undo"

	SettledText = "settled by operator after checking the mailbox"
)

type Resolver struct {
	Actor  string
	Source string
}

func RecordResolution(ctx context.Context, x Execer, who Resolver, ts float64, kind string, rowID int64, proposalID *int64, outcome, prior string) error {
	var proposal any
	if proposalID != nil {
		proposal = *proposalID
	}
	_, err := x.ExecContext(ctx,
		"INSERT INTO write_resolutions (ts, actor, source, kind, row_id, proposal_id, outcome, prior_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ts, who.Actor, who.Source, kind, rowID, proposal, outcome, prior)
	return err
}

const ErrSettledMeanwhile = ProposalError("the write was settled by the operator while it ran; the recorded outcome stands")

type Resolved struct {
	ID      int64
	Settled []int64
}

func (p *Pipeline) Resolve(ctx context.Context, id int64, outcome string, who Resolver) (Resolved, error) {
	if outcome != "" && outcome != OutcomeUndone && outcome != OutcomeNotUndone {
		return Resolved{}, ProposalError("outcome must be " + OutcomeUndone + " or " + OutcomeNotUndone)
	}
	out := Resolved{ID: id, Settled: []int64{}}
	err := p.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, "SELECT status FROM proposals WHERE id = ?", id).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ProposalError(fmt.Sprintf("unknown proposal %d", id))
		}
		if err != nil {
			return err
		}
		if status == Approved {
			return ProposalError(fmt.Sprintf("proposal %d is still being applied", id))
		}
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
			error  string
		}
		blocked, err := db.Collect(rows, func(r *sql.Rows) (held, error) {
			var h held
			return h, r.Scan(&h.id, &h.lease, &h.kind, &h.undoOf, &h.state, &h.error)
		})
		if err != nil {
			return err
		}
		if len(blocked) == 0 {
			return ProposalError(fmt.Sprintf("proposal %d has no blocked write", id))
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
			if err := RecordResolution(ctx, tx, who, now, ResolveProposalAction, h.id, &id, recorded, h.error); err != nil {
				return err
			}
			out.Settled = append(out.Settled, h.id)
		}
		return nil
	})
	return out, err
}
