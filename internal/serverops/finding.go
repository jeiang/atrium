package serverops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const FindingUndoOp = "ops_finding_delete"

func CheckFinding(fact, severity, title, body string, delta []string) error {
	if !slices.Contains(delta, fact) {
		return tool.Error("fact is not in this run's change set")
	}
	if severity != "info" && severity != "warning" && severity != "critical" {
		return tool.Error("severity must be info, warning or critical")
	}
	if title == "" {
		return tool.Error("title is required")
	}
	if body == "" {
		return tool.Error("body is required")
	}
	if len([]rune(title)) > 120 {
		return tool.Error("title is limited to 120 characters")
	}
	if len([]rune(body)) > 1000 {
		return tool.Error("body is limited to 1000 characters")
	}
	return nil
}

type Finding struct {
	Fact, Severity, Title, Body string
	Marks                       tool.Marks
	ThreadID, RunID             int64
	Tool                        string
	Now                         float64
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func WriteFinding(ctx context.Context, store *ledger.Store, f Finding) (int64, error) {
	var id int64
	_, err := store.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: f.Tool, OperationKey: f.Tool + ":" + ledger.NewKey(), Target: "ops_finding:new",
		ThreadID: nullableID(f.ThreadID), RunID: nullableID(f.RunID),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		var err error
		id, err = insertFinding(ctx, tx, f)
		if err != nil {
			return ledger.Result{}, err
		}
		return findingResult(id, f), nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func WriteFindingTx(ctx context.Context, store *ledger.Store, tx *sql.Tx, f Finding) (int64, error) {
	var id int64
	_, err := store.ApplyLocalTx(ctx, tx, ledger.New{
		Origin: "agent", Operation: f.Tool, OperationKey: f.Tool + ":" + ledger.NewKey(), Target: "ops_finding:new",
		ThreadID: nullableID(f.ThreadID), RunID: nullableID(f.RunID),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		var err error
		id, err = insertFinding(ctx, tx, f)
		if err != nil {
			return ledger.Result{}, err
		}
		return findingResult(id, f), nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func insertFinding(ctx context.Context, tx *sql.Tx, f Finding) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO ops_findings (fact, severity, title, body, marks, run_id, thread_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		f.Fact, f.Severity, f.Title, f.Body, int64(f.Marks), nullableID(f.RunID), nullableID(f.ThreadID), f.Now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func findingResult(id int64, f Finding) ledger.Result {
	return ledger.Result{
		State:  ledger.Done,
		Target: "ops_finding:" + fmt.Sprint(id),
		After:  jsonOf(map[string]any{"fact": f.Fact, "severity": f.Severity, "title": f.Title, "body": f.Body}),
		Undo:   jsonOf(findingUndo{Op: FindingUndoOp, ID: id, Applied: f.Now}),
	}
}

func nullableID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

type findingUndo struct {
	Op      string  `json:"op"`
	ID      int64   `json:"id"`
	Applied float64 `json:"applied"`
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var u findingUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, ledger.Refused("finding undo payload is invalid")
	}
	var created float64
	err := q.QueryRowContext(ctx, "SELECT created_at FROM ops_findings WHERE id = ?", u.ID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ledger.Refused(fmt.Sprintf("finding %d is gone", u.ID))
	}
	if err != nil {
		return nil, err
	}
	if created != u.Applied {
		return nil, ledger.Refused(fmt.Sprintf("finding %d changed since this write", u.ID))
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM ops_findings WHERE id = ?", u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": u.ID, "deleted": true}, nil
}

func RegisterLedgerUndoer(s *ledger.Store) error {
	return s.RegisterUndo(FindingUndoOp, ledger.Undoer{
		Validate: func(payload json.RawMessage) error {
			var u findingUndo
			if err := json.Unmarshal(payload, &u); err != nil {
				return ledger.Refused("finding undo payload is invalid")
			}
			if u.Op != FindingUndoOp || u.ID == 0 {
				return ledger.Refused("finding undo payload names " + u.Op)
			}
			return nil
		},
		Local: func(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (ledger.Result, error) {
			value, err := undo(ctx, tx, payload)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
		},
	})
}
