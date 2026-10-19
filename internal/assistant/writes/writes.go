// Package writes is agent/writes.py: the undo log for direct agent writes (Notion, profile, calendar...).
//
// Each write tool records the write with an undo description whose "op" names a registered undoer. Undoers
// re-check that the target is unchanged since the write and refuse otherwise.
package writes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/py"
)

// UndoError is a refused undo.
type UndoError string

func (e UndoError) Error() string { return string(e) }

// Undoer reverses one kind of write. info is the undo description recorded with the write; an UndoError
// means the target changed since or cannot be restored.
type Undoer func(ctx context.Context, info map[string]any) (any, error)

// Registry maps an undo op to its undoer.
type Registry struct {
	mu sync.RWMutex
	by map[string]Undoer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{by: map[string]Undoer{}} }

// Register is register_undoer; a later registration of an op replaces the earlier one.
func (r *Registry) Register(op string, fn Undoer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[op] = fn
}

func (r *Registry) lookup(op string) Undoer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.by[op]
}

// New is the arguments of Record. Before, After and Undo are JSON values (decoded maps, lists, scalars) or
// nil for NULL.
type New struct {
	TS       float64
	ThreadID *int64
	RunID    *int64
	Tool     string
	Target   string
	Before   any
	After    any
	Undo     any
}

func dump(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, err := py.Dumps(value, py.JSONOpts{EnsureASCII: true, SortKeys: true})
	if err != nil {
		return nil, err
	}
	return &text, nil
}

// Record is record: it logs one write and returns its id. Run it in the transaction of the write it logs.
func Record(ctx context.Context, q store.Querier, w New) (int64, error) {
	before, err := dump(w.Before)
	if err != nil {
		return 0, err
	}
	after, err := dump(w.After)
	if err != nil {
		return 0, err
	}
	undo, err := dump(w.Undo)
	if err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO agent_writes (ts, thread_id, run_id, tool, target, before_json, after_json, undo_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		w.TS, w.ThreadID, w.RunID, w.Tool, w.Target, before, after, undo)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Row is an agent_writes row.
type Row struct {
	ID         int64    `json:"id"`
	TS         float64  `json:"ts"`
	ThreadID   *int64   `json:"thread_id"`
	RunID      *int64   `json:"run_id"`
	Tool       string   `json:"tool"`
	Target     string   `json:"target"`
	BeforeJSON *string  `json:"before_json"`
	AfterJSON  *string  `json:"after_json"`
	UndoJSON   *string  `json:"undo_json"`
	UndoneAt   *float64 `json:"undone_at"`
}

// Get is get: nil when there is no such write.
func Get(ctx context.Context, q store.Querier, writeID int64) (*Row, error) {
	var r Row
	err := q.QueryRowContext(ctx,
		"SELECT id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at FROM agent_writes WHERE id = ?", writeID).
		Scan(&r.ID, &r.TS, &r.ThreadID, &r.RunID, &r.Tool, &r.Target, &r.BeforeJSON, &r.AfterJSON, &r.UndoJSON, &r.UndoneAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Undo is undo: it runs the write's undoer and marks the write undone, returning the undoer's result. now
// is read after the undoer ran.
func (r *Registry) Undo(ctx context.Context, q store.Querier, writeID int64, now func() float64) (any, error) {
	row, err := Get(ctx, q, writeID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, UndoError(fmt.Sprintf("no write %d", writeID))
	}
	if row.UndoneAt != nil {
		return nil, UndoError("already undone")
	}
	var info any
	if row.UndoJSON != nil && *row.UndoJSON != "" {
		if err := json.Unmarshal([]byte(*row.UndoJSON), &info); err != nil {
			return nil, fmt.Errorf("writes: write %d undo_json: %w", writeID, err)
		}
	}
	if !py.Truthy(info) {
		return nil, UndoError("this write has no undo")
	}
	desc, _ := info.(map[string]any)
	op, _ := desc["op"].(string)
	fn := r.lookup(op)
	if fn == nil {
		shown := "None"
		if s, ok := desc["op"].(string); ok {
			shown = py.Repr(s)
		}
		return nil, UndoError("unknown undo kind " + shown)
	}
	result, err := fn(ctx, desc)
	if err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, "UPDATE agent_writes SET undone_at = ? WHERE id = ? AND undone_at IS NULL", now(), writeID); err != nil {
		return nil, err
	}
	return result, nil
}
