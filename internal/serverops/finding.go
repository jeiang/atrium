package serverops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
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

func WriteFinding(ctx context.Context, d *db.DB, reg *writes.Registry, f Finding) (int64, error) {
	var id int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = writeFindingTx(ctx, tx, reg, f)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func writeFindingTx(ctx context.Context, tx *sql.Tx, reg *writes.Registry, f Finding) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO ops_findings (fact, severity, title, body, marks, run_id, thread_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		f.Fact, f.Severity, f.Title, f.Body, int64(f.Marks), nullableID(f.RunID), nullableID(f.ThreadID), f.Now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	w := writes.New{
		TS: f.Now, Tool: f.Tool, Target: "ops_finding:" + fmt.Sprint(id),
		After: map[string]any{"fact": f.Fact, "severity": f.Severity, "title": f.Title, "body": f.Body},
		Undo:  findingUndo{Op: FindingUndoOp, ID: id, Applied: f.Now},
	}
	if f.ThreadID != 0 {
		w.ThreadID = &f.ThreadID
	}
	if f.RunID != 0 {
		w.RunID = &f.RunID
	}
	if _, err := writes.Record(ctx, tx, reg, w); err != nil {
		return 0, err
	}
	return id, nil
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

type findingUndo struct {
	Op      string  `json:"op"`
	ID      int64   `json:"id"`
	Applied float64 `json:"applied"`
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var u findingUndo
	if err := json.Unmarshal(payload, &u); err != nil {
		return nil, writes.UndoError("finding undo payload is invalid")
	}
	var created float64
	err := q.QueryRowContext(ctx, "SELECT created_at FROM ops_findings WHERE id = ?", u.ID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, writes.UndoError(fmt.Sprintf("finding %d is gone", u.ID))
	}
	if err != nil {
		return nil, err
	}
	if created != u.Applied {
		return nil, writes.UndoError(fmt.Sprintf("finding %d changed since this write", u.ID))
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM ops_findings WHERE id = ?", u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": u.ID, "deleted": true}, nil
}

func RegisterUndoer(r *writes.Registry) { r.Register(FindingUndoOp, undo) }

var findingUndoers = func() *writes.Registry {
	r := writes.NewRegistry()
	RegisterUndoer(r)
	return r
}()
