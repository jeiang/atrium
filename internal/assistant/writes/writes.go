package writes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"

	"github.com/jeiang/atrium/internal/py"
)

type UndoError string

func (e UndoError) Error() string { return string(e) }

type Undoer func(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error)

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

func (r *Registry) Has(op string) bool { return r.lookup(op) != nil }

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

func dump(value any, scrub bool) (*string, error) {
	if value == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(plain(value)); err != nil {
		return nil, fmt.Errorf("writes: payload: %w", err)
	}
	text := strings.TrimSuffix(buf.String(), "\n")
	if scrub {
		text = logx.Scrub(text)
	}
	return &text, nil
}

func plain(value any) any {
	switch v := value.(type) {
	case py.Dict:
		out := make(map[string]any, len(v))
		for _, p := range v {
			out[p.Key] = plain(p.Value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = plain(e)
		}
		return out
	}
	return value
}

func payloads(w New) (before, after, undo *string, err error) {
	if before, err = dump(w.Before, true); err != nil {
		return nil, nil, nil, err
	}
	if after, err = dump(w.After, true); err != nil {
		return nil, nil, nil, err
	}
	if undo, err = dump(w.Undo, false); err != nil {
		return nil, nil, nil, err
	}
	return before, after, undo, nil
}

func validateUndo(reg *Registry, undo *string) error {
	if undo == nil {
		return nil
	}
	var desc map[string]any
	if err := json.Unmarshal([]byte(*undo), &desc); err != nil {
		return fmt.Errorf("writes: undo payload: %w", err)
	}
	op, _ := desc["op"].(string)
	if op == "" {
		return errors.New("writes: undo payload without an op")
	}
	if reg == nil || reg.lookup(op) == nil {
		return UndoError("unknown undo kind " + strconv.Quote(op))
	}
	return nil
}

func Record(ctx context.Context, q db.Querier, reg *Registry, w New) (int64, error) {
	before, after, undo, err := payloads(w)
	if err != nil {
		return 0, err
	}
	if err := validateUndo(reg, undo); err != nil {
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

func Begin(ctx context.Context, q db.Querier, w New) (int64, error) {
	before, after, undo, err := payloads(w)
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

func Settle(ctx context.Context, q db.Querier, reg *Registry, id int64, w New) error {
	before, after, undo, err := payloads(w)
	if err != nil {
		return err
	}
	if err := validateUndo(reg, undo); err != nil {
		return err
	}
	res, err := q.ExecContext(ctx,
		"UPDATE agent_writes SET target = ?, before_json = ?, after_json = ?, undo_json = ? WHERE id = ?",
		w.Target, before, after, undo, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return UndoError("write intent " + strconv.FormatInt(id, 10) + " is gone")
	}
	return nil
}

func Discard(ctx context.Context, q db.Querier, id int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM agent_writes WHERE id = ? AND undo_json IS NULL", id)
	return err
}

type row struct {
	ID         int64
	TS         float64
	ThreadID   *int64
	RunID      *int64
	Tool       string
	Target     string
	BeforeJSON *string
	AfterJSON  *string
	UndoJSON   *string
	UndoneAt   *float64
}

func get(ctx context.Context, q db.Querier, writeID int64) (*row, error) {
	var r row
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

func (r *Registry) resolve(ctx context.Context, q db.Querier, writeID int64) (Undoer, json.RawMessage, error) {
	row, err := get(ctx, q, writeID)
	if err != nil {
		return nil, nil, err
	}
	if row == nil {
		return nil, nil, UndoError(fmt.Sprintf("no write %d", writeID))
	}
	if row.UndoneAt != nil {
		return nil, nil, UndoError("already undone")
	}
	var raw json.RawMessage
	if row.UndoJSON != nil && *row.UndoJSON != "" {
		raw = json.RawMessage(*row.UndoJSON)
	}
	if len(raw) == 0 {
		return nil, nil, UndoError("this write has no undo")
	}
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, nil, fmt.Errorf("writes: write %d undo_json: %w", writeID, err)
	}
	fn := r.lookup(head.Op)
	if fn == nil {
		return nil, nil, UndoError("unknown undo kind " + strconv.Quote(head.Op))
	}
	return fn, raw, nil
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
	fn, payload, err := r.resolve(ctx, d.Read, writeID)
	if err != nil {
		return nil, err
	}
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := fn(ctx, tx, payload)
	if err != nil {
		return nil, err
	}
	if err := mark(ctx, tx, writeID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
