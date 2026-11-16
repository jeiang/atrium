package attention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

func Snooze(ctx context.Context, q db.Querier, kind, sourceID string, until, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO attention_snoozes (kind, source_id, until, created_ts) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(kind, source_id) DO UPDATE SET until = excluded.until, created_ts = excluded.created_ts",
		kind, sourceID, until, now)
	return err
}

func Unsnooze(ctx context.Context, q db.Querier, kind, sourceID string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM attention_snoozes WHERE kind = ? AND source_id = ?", kind, sourceID)
	return err
}

func snoozed(ctx context.Context, q db.Querier, now float64) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, source_id FROM attention_snoozes WHERE until > ?", now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	hidden := map[string]bool{}
	for rows.Next() {
		var kind, sourceID string
		if err := rows.Scan(&kind, &sourceID); err != nil {
			return nil, err
		}
		hidden[kind+"\x00"+sourceID] = true
	}
	return hidden, rows.Err()
}

type SnoozeUndo struct {
	Op       string   `json:"op"`
	Kind     string   `json:"kind"`
	SourceID string   `json:"source_id"`
	Until    *float64 `json:"until"`
}

func SnoozedUntil(ctx context.Context, q db.Querier, kind, sourceID string) (*float64, error) {
	var until float64
	err := q.QueryRowContext(ctx, "SELECT until FROM attention_snoozes WHERE kind = ? AND source_id = ?", kind, sourceID).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &until, nil
}

func ActiveSnoozes(ctx context.Context, q db.Querier, now float64) (map[string]float64, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, source_id, until FROM attention_snoozes WHERE until > ?", now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]float64{}
	for rows.Next() {
		var kind, sourceID string
		var until float64
		if err := rows.Scan(&kind, &sourceID, &until); err != nil {
			return nil, err
		}
		out[kind+"\x00"+sourceID] = until
	}
	return out, rows.Err()
}

func SnoozeUndoDefinition(op string, now func() float64) ledger.UndoDefinition {
	return ledger.UndoDefinition{Operation: op, Undoer: ledger.Undoer{
		Validate: func(payload json.RawMessage) error {
			var undo SnoozeUndo
			if err := json.Unmarshal(payload, &undo); err != nil || undo.Op != op || undo.Kind == "" || undo.SourceID == "" {
				return ledger.Refused("snooze undo payload is invalid")
			}
			return nil
		},
		Local: func(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (ledger.Result, error) {
			var undo SnoozeUndo
			if err := json.Unmarshal(payload, &undo); err != nil {
				return ledger.Result{}, err
			}
			var err error
			if undo.Until == nil {
				err = Unsnooze(ctx, tx, undo.Kind, undo.SourceID)
			} else {
				err = Snooze(ctx, tx, undo.Kind, undo.SourceID, *undo.Until, now())
			}
			if err != nil {
				return ledger.Result{}, err
			}
			after, err := json.Marshal(map[string]any{"snoozed_until": undo.Until})
			return ledger.Result{State: ledger.Done, After: after}, err
		},
	}}
}

const (
	UndoTaskSnooze       = "task_snooze"
	UndoCommitmentSnooze = "commitment_snooze"
)
