package writes

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jeiang/atrium/internal/db"

	"github.com/jeiang/atrium/internal/py"
)

type UndoError string

func (e UndoError) Error() string { return string(e) }

type Undoer func(ctx context.Context, q db.Querier, info map[string]any) (any, error)

type Registry struct {
	mu sync.RWMutex
	by map[string]Undoer
}

func NewRegistry() *Registry { return &Registry{by: map[string]Undoer{}} }

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
	text := py.JSONDumps(value, py.JSONOpts{SortKeys: true})
	return &text, nil
}

func Record(ctx context.Context, q db.Querier, w New) (int64, error) {
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

func Get(ctx context.Context, q db.Querier, writeID int64) (*Row, error) {
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

type lazyTx struct {
	d  *db.DB
	tx *sql.Tx
}

func (l *lazyTx) begin(ctx context.Context) (*sql.Tx, error) {
	if l.tx == nil {
		tx, err := l.d.Write.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		l.tx = tx
	}
	return l.tx, nil
}

func (l *lazyTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	return tx.ExecContext(ctx, query, args...)
}

func (l *lazyTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	return tx.QueryContext(ctx, query, args...)
}

func (l *lazyTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx, err := l.begin(ctx)
	if err != nil {
		failed := sql.OpenDB(failedConn{err})
		defer func() { _ = failed.Close() }()
		return failed.QueryRowContext(ctx, "SELECT 1")
	}
	return tx.QueryRowContext(ctx, query, args...)
}

type failedConn struct{ err error }

func (f failedConn) Connect(context.Context) (driver.Conn, error) { return nil, f.err }
func (f failedConn) Driver() driver.Driver                        { return nil }

func (l *lazyTx) rollback() {
	if l.tx != nil {
		_ = l.tx.Rollback()
	}
}

type idLocks struct {
	mu sync.Mutex
	by map[int64]*idLock
}

type idLock struct {
	sync.Mutex
	refs int
}

func (l *idLocks) lock(id int64) func() {
	l.mu.Lock()
	if l.by == nil {
		l.by = map[int64]*idLock{}
	}
	e := l.by[id]
	if e == nil {
		e = &idLock{}
		l.by[id] = e
	}
	e.refs++
	l.mu.Unlock()
	e.Lock()
	return func() {
		e.Unlock()
		l.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(l.by, id)
		}
		l.mu.Unlock()
	}
}

var undoing idLocks

func (r *Registry) resolve(ctx context.Context, q db.Querier, writeID int64) (Undoer, map[string]any, error) {
	row, err := Get(ctx, q, writeID)
	if err != nil {
		return nil, nil, err
	}
	if row == nil {
		return nil, nil, UndoError(fmt.Sprintf("no write %d", writeID))
	}
	if row.UndoneAt != nil {
		return nil, nil, UndoError("already undone")
	}
	var info any
	if row.UndoJSON != nil && *row.UndoJSON != "" {
		if err := json.Unmarshal([]byte(*row.UndoJSON), &info); err != nil {
			return nil, nil, fmt.Errorf("writes: write %d undo_json: %w", writeID, err)
		}
	}
	if !py.Truthy(info) {
		return nil, nil, UndoError("this write has no undo")
	}
	desc, _ := info.(map[string]any)
	op, _ := desc["op"].(string)
	fn := r.lookup(op)
	if fn == nil {
		shown := "None"
		if s, ok := desc["op"].(string); ok {
			shown = py.Repr(s)
		}
		return nil, nil, UndoError("unknown undo kind " + shown)
	}
	return fn, desc, nil
}

func mark(ctx context.Context, q db.Querier, writeID int64, now func() float64) error {
	res, err := q.ExecContext(ctx, "UPDATE agent_writes SET undone_at = ? WHERE id = ? AND undone_at IS NULL", now(), writeID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return UndoError("already undone")
	}
	return nil
}

func (r *Registry) Undo(ctx context.Context, d *db.DB, writeID int64, now func() float64) (any, error) {
	defer undoing.lock(writeID)()
	fn, desc, err := r.resolve(ctx, d.Read, writeID)
	if err != nil {
		return nil, err
	}
	l := &lazyTx{d: d}
	defer l.rollback()
	result, err := fn(ctx, l, desc)
	if err != nil {
		return nil, err
	}
	if err := mark(ctx, l, writeID, now); err != nil {
		return nil, err
	}
	if err := l.tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Registry) UndoIn(ctx context.Context, q db.Querier, writeID int64, now func() float64) (any, error) {
	fn, desc, err := r.resolve(ctx, q, writeID)
	if err != nil {
		return nil, err
	}
	result, err := fn(ctx, q, desc)
	if err != nil {
		return nil, err
	}
	if err := mark(ctx, q, writeID, now); err != nil {
		return nil, err
	}
	return result, nil
}
