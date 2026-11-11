package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) Undo(ctx context.Context, id int64, who Resolver) (Effect, error) {
	if strings.TrimSpace(who.Actor) == "" {
		return Effect{}, Refused("an undo needs an actor")
	}
	if who.Source != "cli" && who.Source != "ui" {
		return Effect{}, Refused("an undo source must be cli or ui")
	}
	forward, err := s.Get(ctx, id)
	if err != nil {
		return Effect{}, err
	}
	fn, payload, err := s.undoPlan(forward)
	if err != nil {
		return Effect{}, err
	}
	if fn.Local != nil {
		return s.undoLocal(ctx, id, fn, payload)
	}
	ticket, err := s.reserveUndo(ctx, id, payload)
	if err != nil {
		return Effect{}, err
	}
	stop := s.KeepAlive(ctx, []Ticket{ticket})
	result, err := fn.External(ctx, payload)
	stop()
	settleCtx := context.WithoutCancel(ctx)
	if err != nil {
		var refused Refused
		if errors.As(err, &refused) {
			_ = s.Settle(settleCtx, ticket, Result{State: Failed, Error: err.Error()}, nil)
			return Effect{}, err
		}
		result = Result{State: Unknown, Error: err.Error()}
	}
	var finalize Mutation
	if fn.Finalize != nil {
		finalize = func(ctx context.Context, tx *sql.Tx) error {
			return fn.Finalize(ctx, tx, payload, result)
		}
	}
	if fn.After != nil {
		before, after := finalize, fn.After
		finalize = func(ctx context.Context, tx *sql.Tx) error {
			if before != nil {
				if err := before(ctx, tx); err != nil {
					return err
				}
			}
			return after(ctx, tx, forward, result)
		}
	}
	if err := s.Settle(settleCtx, ticket, result, finalize); err != nil {
		return Effect{}, err
	}
	return s.Get(settleCtx, ticket.ID)
}

func (s *Store) undoPlan(forward Effect) (Undoer, json.RawMessage, error) {
	if forward.Direction != "forward" {
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d is itself an undo", forward.ID))
	}
	if forward.UndoneBy != nil {
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d is already undone", forward.ID))
	}
	if forward.Resolution != nil && forward.Resolution.EffectiveOutcome == "failed" {
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d was resolved as not applied", forward.ID))
	}
	switch forward.State {
	case Done, Partial:
	case Unknown:
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d has an unknown outcome; resolve it first", forward.ID))
	case Intent:
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d may still be running; wait until atrium is idle and try again", forward.ID))
	default:
		return Undoer{}, nil, Refused(fmt.Sprintf("effect %d has no undoable outcome", forward.ID))
	}
	if len(forward.Undo) == 0 {
		return Undoer{}, nil, Refused("this effect has no undo")
	}
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(forward.Undo, &head); err != nil {
		return Undoer{}, nil, fmt.Errorf("ledger: effect %d undo payload: %w", forward.ID, err)
	}
	fn, ok := s.undoer(head.Op)
	if !ok {
		return Undoer{}, nil, Refused("unknown undo kind " + fmt.Sprintf("%q", head.Op))
	}
	if fn.Validate != nil {
		if err := fn.Validate(forward.Undo); err != nil {
			return Undoer{}, nil, err
		}
	}
	return fn, forward.Undo, nil
}

func (s *Store) undoLocal(ctx context.Context, id int64, fn Undoer, payload json.RawMessage) (Effect, error) {
	var undoID int64
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		ticket, err := s.reserveUndoTx(ctx, tx, id, payload)
		if err != nil {
			return err
		}
		undoID = ticket.ID
		result, err := fn.Local(ctx, tx, payload)
		if err != nil {
			return err
		}
		if !result.State.Final() {
			return fmt.Errorf("ledger: undo %d cannot settle as %q", undoID, result.State)
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE effects SET state = ?, settled_at = ?, after_json = COALESCE(?, after_json), error = ? "+
				"WHERE id = ? AND token = ? AND state = 'intent'",
			string(result.State), s.now(), rawText(result.After), nullText(result.Error), undoID, ticket.Token)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return &SettledMeanwhile{ID: undoID}
		}
		if fn.After != nil {
			forward, err := scanEffect(tx.QueryRowContext(ctx, effectSelect+" WHERE e.id = ?", id))
			if err != nil {
				return err
			}
			if err := fn.After(ctx, tx, forward, result); err != nil {
				return err
			}
		}
		return s.afterTransition(ctx, tx, undoID, "undo", result.State)
	})
	if err != nil {
		return Effect{}, err
	}
	return s.Get(context.WithoutCancel(ctx), undoID)
}

func (s *Store) reserveUndo(ctx context.Context, id int64, payload json.RawMessage) (Ticket, error) {
	var ticket Ticket
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		ticket, err = s.reserveUndoTx(ctx, tx, id, payload)
		return err
	})
	return ticket, err
}

func (s *Store) reserveUndoTx(ctx context.Context, tx *sql.Tx, id int64, payload json.RawMessage) (Ticket, error) {
	forward, err := scanEffect(tx.QueryRowContext(ctx, effectSelect+" WHERE e.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, &NotFound{ID: id}
	}
	if err != nil {
		return Ticket{}, err
	}
	if _, _, err := s.undoPlan(forward); err != nil {
		return Ticket{}, err
	}
	var active int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM effects u WHERE u.undo_of = ? AND u.state IN ('intent','unknown','partial','done') "+
			"AND NOT EXISTS (SELECT 1 FROM effect_resolutions ur WHERE ur.effect_id = u.id AND ur.effective_outcome = 'failed')",
		id).Scan(&active); err != nil {
		return Ticket{}, err
	}
	if active > 0 {
		return Ticket{}, Refused(fmt.Sprintf("effect %d already has an undo in flight or done", id))
	}
	token, err := newToken()
	if err != nil {
		return Ticket{}, err
	}
	now := s.now()
	res, err := tx.ExecContext(ctx,
		"INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, "+
			"proposal_id, thread_id, run_id, account, message_id, digest, decision_id, src, dest, target, action_json, "+
			"before_json, undo_json, state, settled_at, error) "+
			"VALUES (?, ?, ?, ?, ?, ?, 'undo', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 'intent', NULL, NULL)",
		now, now, token, forward.Origin, undoOperation(payload, forward.Operation), forward.OperationKey, id,
		forward.ProposalID, forward.ThreadID, forward.RunID, nullText(forward.Account), forward.MessageID, forward.Digest,
		forward.DecisionID, nullText(forward.Src), nullText(forward.Dest), forward.Target, string(payload),
		rawText(forward.After))
	if err != nil {
		return Ticket{}, err
	}
	undoID, err := res.LastInsertId()
	if err != nil {
		return Ticket{}, err
	}
	keys, err := effectKeys(ctx, tx, id)
	if err != nil {
		return Ticket{}, err
	}
	for _, key := range keys {
		var others int64
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM effect_resources WHERE resource = ? AND holding = 1 AND effect_id <> ?", key, id).
			Scan(&others); err != nil {
			return Ticket{}, err
		}
		if others > 0 {
			return Ticket{}, &Conflict{Resource: key}
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO effect_claims (resource) VALUES (?)", key); err != nil {
			return Ticket{}, err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, 0)", undoID, key); err != nil {
			return Ticket{}, err
		}
	}
	return Ticket{ID: undoID, Token: token}, nil
}

func undoOperation(payload json.RawMessage, fallback string) string {
	var head struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(payload, &head) == nil && head.Op != "" {
		return head.Op
	}
	return fallback
}
