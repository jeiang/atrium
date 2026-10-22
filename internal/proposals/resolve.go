package proposals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mailops"
)

const (
	SettledText      = "settled by operator after checking the mailbox"
	OutcomeUndone    = "undone"
	OutcomeNotUndone = "not-undone"
	OutcomeSettled   = "settled"

	ResolveProposalAction = "proposal_action"
	ResolveAutoUndo       = "auto_undo"
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

func Blocked(ctx context.Context, q Querier, id int64) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM proposal_actions a WHERE a.proposal_id = ? AND a.ok = 0 AND "+
		"(instr(a.error, '"+mailops.UnknownPrefix+"') > 0 OR instr(a.error, '"+mailops.PartialPrefix+"') > 0))", id).Scan(&blocked)
	return blocked, err
}

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
		rows, err := tx.QueryContext(ctx, "SELECT id, ts, json_extract(action, '$.op'), error FROM proposal_actions "+
			"WHERE proposal_id = ? AND ok = 0 AND (instr(error, ?) > 0 OR instr(error, ?) > 0) ORDER BY id",
			id, mailops.UnknownPrefix, mailops.PartialPrefix)
		if err != nil {
			return err
		}
		type held struct {
			id    int64
			ts    float64
			op    *string
			error string
		}
		blocked, err := db.Collect(rows, func(r *sql.Rows) (held, error) {
			var h held
			return h, r.Scan(&h.id, &h.ts, &h.op, &h.error)
		})
		if err != nil {
			return err
		}
		if len(blocked) == 0 {
			return ProposalError(fmt.Sprintf("proposal %d has no blocked write", id))
		}
		now := p.now()
		for _, h := range blocked {
			isUndo := h.op != nil && *h.op == UndoOp
			switch {
			case h.error == InFlightText && now-h.ts < StuckSeconds:
				return ProposalError(fmt.Sprintf("action %d may still be running; wait until atrium is idle and try again", h.id))
			case isUndo && outcome == "":
				return ProposalError(fmt.Sprintf("action %d is a blocked undo; check the mailbox and say whether the message is %s or %s", h.id, OutcomeUndone, OutcomeNotUndone))
			}
		}
		for _, h := range blocked {
			recorded := OutcomeSettled
			if h.op != nil && *h.op == UndoOp {
				recorded = outcome
				if outcome == OutcomeUndone {
					_, err = tx.ExecContext(ctx, "UPDATE proposal_actions SET ok = 1, error = NULL WHERE id = ?", h.id)
				} else {
					_, err = tx.ExecContext(ctx, "UPDATE proposal_actions SET error = ? WHERE id = ?", SettledText, h.id)
				}
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE proposal_actions SET error = ? WHERE id = ?", SettledText, h.id)
			}
			if err != nil {
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
