package tasks

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
)

const (
	StatusOpen      = "open"
	StatusDone      = "done"
	StatusDismissed = "dismissed"
)

type Task struct {
	ID         int64    `json:"id"`
	Title      string   `json:"title"`
	Notes      string   `json:"notes"`
	Due        *string  `json:"due"`
	RemindAt   *float64 `json:"remind_at"`
	Status     string   `json:"status"`
	Priority   string   `json:"priority"`
	SourceKind string   `json:"source_kind"`
	SourceID   string   `json:"source_id"`
	CreatedTS  float64  `json:"created_ts"`
	UpdatedTS  float64  `json:"updated_ts"`
	DoneTS     *float64 `json:"done_ts"`
	RemindedTS *float64 `json:"-"`
}

const (
	LinkThread   = "thread"
	LinkProject  = "project"
	LinkPerson   = "person"
	LinkRecord   = "record"
	LinkMessage  = "message"
	LinkProposal = "proposal"
)

var SettableLinks = []string{LinkThread, LinkProject, LinkPerson, LinkRecord, LinkMessage}

type Link struct {
	Kind  string  `json:"kind"`
	ID    string  `json:"id"`
	Title *string `json:"title" api:"nullable"`
}

type Spec struct {
	Title      string   `json:"title"`
	Notes      string   `json:"notes"`
	Due        *string  `json:"due"`
	RemindAt   *float64 `json:"remind_at"`
	Priority   string   `json:"priority"`
	SourceKind string   `json:"source_kind"`
	SourceID   string   `json:"source_id"`
}

const columns = "id, title, notes, due, remind_at, status, priority, source_kind, source_id, created_ts, updated_ts, done_ts, reminded_ts"

func scan(r interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := r.Scan(&t.ID, &t.Title, &t.Notes, &t.Due, &t.RemindAt, &t.Status, &t.Priority, &t.SourceKind, &t.SourceID, &t.CreatedTS, &t.UpdatedTS, &t.DoneTS, &t.RemindedTS)
	return t, err
}

func Add(ctx context.Context, q db.Querier, now float64, s Spec) (int64, error) {
	if s.Title == "" {
		return 0, errors.New("a task needs a title")
	}
	priority := s.Priority
	if priority == "" {
		priority = "normal"
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO tasks (title, notes, due, remind_at, status, priority, source_kind, source_id, created_ts, updated_ts) "+
			"VALUES (?, ?, ?, ?, 'open', ?, ?, ?, ?, ?)",
		s.Title, s.Notes, s.Due, s.RemindAt, priority, s.SourceKind, s.SourceID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func Get(ctx context.Context, q db.Querier, id int64) (*Task, error) {
	t, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM tasks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func Find(ctx context.Context, q db.Querier, text string, limit, offset int) ([]Task, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE instr(lower(title), lower(?1)) > 0 OR instr(lower(notes), lower(?1)) > 0 "+
		"ORDER BY instr(lower(title), lower(?1)) > 0 DESC, updated_ts DESC, id DESC LIMIT ?2 OFFSET ?3", text, limit, offset)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Task, error) { return scan(r) })
}

func OfSource(ctx context.Context, q db.Querier, kind, id string) ([]Task, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM tasks WHERE source_kind = ? AND source_id = ? "+
		"ORDER BY CASE status WHEN 'open' THEN 0 ELSE 1 END, COALESCE(remind_at, 1e18), created_ts, id", kind, id)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Task, error) { return scan(r) })
}

func Update(ctx context.Context, q db.Querier, now float64, id int64, s Spec) (bool, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE tasks SET title = ?, notes = ?, due = ?, remind_at = ?, priority = ?, source_kind = ?, source_id = ?, "+
			"reminded_ts = CASE WHEN remind_at IS ? THEN reminded_ts ELSE NULL END, updated_ts = ? WHERE id = ?",
		s.Title, s.Notes, s.Due, s.RemindAt, s.Priority, s.SourceKind, s.SourceID, s.RemindAt, now, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func Done(ctx context.Context, q db.Querier, now float64, id int64) (bool, error) {
	res, err := q.ExecContext(ctx, "UPDATE tasks SET status = 'done', done_ts = ?, updated_ts = ? WHERE id = ? AND status = 'open'", now, now, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func Reopen(ctx context.Context, q db.Querier, now float64, id int64) (bool, error) {
	return db.Changed(q.ExecContext(ctx, "UPDATE tasks SET status = 'open', done_ts = NULL, updated_ts = ? WHERE id = ? AND status <> 'open'", now, id))
}

func Dismiss(ctx context.Context, q db.Querier, now float64, id int64) (bool, error) {
	return db.Changed(q.ExecContext(ctx, "UPDATE tasks SET status = 'dismissed', updated_ts = ? WHERE id = ? AND status = 'open'", now, id))
}

func Reinsert(ctx context.Context, q db.Querier, t Task) (bool, error) {
	return db.Changed(q.ExecContext(ctx,
		"INSERT INTO tasks ("+columns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING",
		t.ID, t.Title, t.Notes, t.Due, t.RemindAt, t.Status, t.Priority, t.SourceKind, t.SourceID, t.CreatedTS, t.UpdatedTS, t.DoneTS, t.RemindedTS))
}

func Delete(ctx context.Context, q db.Querier, id int64) (bool, error) {
	res, err := q.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func IDString(id int64) string { return strconv.FormatInt(id, 10) }
