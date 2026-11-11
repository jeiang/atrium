package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

func (s *Store) Resolve(ctx context.Context, id int64, r Resolution, who Resolver, local Mutation) (Effect, error) {
	ctx = context.WithoutCancel(ctx)
	if strings.TrimSpace(who.Actor) == "" {
		return Effect{}, Refused("a resolution needs an actor")
	}
	switch who.Source {
	case "cli", "ui", "phone", "import":
	default:
		return Effect{}, Refused("a resolution source must be cli, ui, phone or import")
	}
	now := s.now()
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		e, err := scanEffect(tx.QueryRowContext(ctx, effectSelect+" WHERE e.id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return &NotFound{ID: id}
		}
		if err != nil {
			return err
		}
		priorState := string(e.State)
		priorError := e.Error
		switch e.State {
		case Intent:
			if now-e.LeaseAt < StuckSeconds {
				return Refused(fmt.Sprintf("effect %d may still be running; wait until atrium is idle and try again", id))
			}
			res, err := tx.ExecContext(ctx,
				"UPDATE effects SET state = 'unknown', settled_at = ?, token = lower(hex(randomblob(16))), "+
					"error = COALESCE(error, 'the attempt was interrupted; the mutation may have happened') "+
					"WHERE id = ? AND state = 'intent'", now, id)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n == 0 {
				return &SettledMeanwhile{ID: id}
			}
		case Unknown, Partial:
		default:
			return Refused(fmt.Sprintf("effect %d has no unknown or partial outcome", id))
		}
		effective, ok := effectiveOutcome(e.Direction, r.Outcome)
		if !ok {
			return Refused(outcomeHelp(e.Direction))
		}
		if e.Resolution != nil {
			if e.Resolution.EffectiveOutcome != "partial" {
				return Refused(fmt.Sprintf("effect %d is already resolved as %s", id, e.Resolution.EffectiveOutcome))
			}
			started, err := dependentStarted(ctx, tx, e)
			if err != nil {
				return err
			}
			if started {
				return Refused(fmt.Sprintf("effect %d has a dependent operation; its earlier resolution can no longer be refined", id))
			}
		}
		if len(r.Undo) > 0 {
			if e.Direction != "forward" || (r.Outcome != "applied" && r.Outcome != "partial") {
				return Refused("an inverse can only be attached to an applied or partial forward resolution")
			}
			if err := s.validateUndo(r.Undo); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO effect_resolutions (effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, "+
				"prior_state, prior_error, evidence_json, legacy_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)",
			id, e.ProposalID, now, who.Actor, who.Source, r.Outcome, effective, priorState, priorError, rawText(r.Evidence)); err != nil {
			return err
		}
		if len(r.Undo) > 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE effects SET undo_json = ? WHERE id = ?", string(r.Undo), id); err != nil {
				return err
			}
		}
		if local != nil {
			if err := local(ctx, tx); err != nil {
				return err
			}
		}
		if hook, ok := s.resolveHook(e.Operation); ok {
			if err := hook(ctx, tx, e, r); err != nil {
				return err
			}
		}
		switch e.Direction {
		case "forward":
			switch effective {
			case "done":
				return release(ctx, tx, id, false)
			case "failed":
				return release(ctx, tx, id, true)
			}
		case "undo":
			switch effective {
			case "done":
				return completeUndo(ctx, tx, id)
			case "failed":
				return release(ctx, tx, id, true)
			}
		}
		return nil
	})
	if err != nil {
		return Effect{}, err
	}
	return s.Get(context.WithoutCancel(ctx), id)
}

func AuditProposal(ctx context.Context, x db.Querier, ts float64, who Resolver, proposalID int64, outcome string) error {
	if strings.TrimSpace(who.Actor) == "" {
		return Refused("a resolution needs an actor")
	}
	switch who.Source {
	case "cli", "ui", "phone", "import":
	default:
		return Refused("a resolution source must be cli, ui, phone or import")
	}
	if strings.TrimSpace(outcome) == "" {
		return Refused("an approval audit needs an outcome")
	}
	_, err := x.ExecContext(context.WithoutCancel(ctx),
		"INSERT INTO effect_resolutions (effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, "+
			"prior_state, prior_error, evidence_json, legacy_json) VALUES (NULL, ?, ?, ?, ?, ?, NULL, NULL, '', NULL, NULL)",
		proposalID, ts, who.Actor, who.Source, outcome)
	return err
}

func (s *Store) SetUndo(ctx context.Context, tx *sql.Tx, id int64, undo json.RawMessage) error {
	if err := s.validateUndo(undo); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE effects SET undo_json = ? WHERE id = ?", string(undo), id)
	return err
}

func effectiveOutcome(direction, outcome string) (string, bool) {
	if direction == "undo" {
		switch outcome {
		case "undone":
			return "done", true
		case "not-undone":
			return "failed", true
		case "partial":
			return "partial", true
		}
		return "", false
	}
	switch outcome {
	case "applied":
		return "done", true
	case "not-applied":
		return "failed", true
	case "partial":
		return "partial", true
	}
	return "", false
}

func outcomeHelp(direction string) string {
	if direction == "undo" {
		return "outcome must be undone, not-undone or partial"
	}
	return "outcome must be applied, not-applied or partial"
}

func dependentStarted(ctx context.Context, tx *sql.Tx, e Effect) (bool, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM effects WHERE undo_of = ?", e.ID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM effects WHERE direction = 'forward' AND operation_key = ? AND id > ?",
		e.OperationKey, e.ID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
