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

const columns = "id, title, notes, due, remind_at, status, priority, source_kind, source_id, created_ts, updated_ts, done_ts"

func scan(r interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := r.Scan(&t.ID, &t.Title, &t.Notes, &t.Due, &t.RemindAt, &t.Status, &t.Priority, &t.SourceKind, &t.SourceID, &t.CreatedTS, &t.UpdatedTS, &t.DoneTS)
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

type ListKey struct {
	Rank      int     `json:"r"`
	RemindAt  float64 `json:"a"`
	CreatedTS float64 `json:"c"`
	ID        int64   `json:"id"`
}

func KeyOf(t Task) ListKey {
	k := ListKey{RemindAt: 1e18, CreatedTS: t.CreatedTS, ID: t.ID}
	if t.Status != StatusOpen {
		k.Rank = 1
	}
	if t.RemindAt != nil {
		k.RemindAt = *t.RemindAt
	}
	return k
}

func List(ctx context.Context, q db.Querier, status string, limit int, after *ListKey) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := "SELECT " + columns + " FROM tasks WHERE 1 = 1"
	var args []any
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	if after != nil {
		query += " AND (CASE status WHEN 'open' THEN 0 ELSE 1 END, COALESCE(remind_at, 1e18), created_ts, id) > (?, ?, ?, ?)"
		args = append(args, after.Rank, after.RemindAt, after.CreatedTS, after.ID)
	}
	query += " ORDER BY CASE status WHEN 'open' THEN 0 ELSE 1 END, COALESCE(remind_at, 1e18), created_ts, id LIMIT ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Task, error) { return scan(r) })
}

func Update(ctx context.Context, q db.Querier, now float64, id int64, s Spec) (bool, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE tasks SET title = ?, notes = ?, due = ?, remind_at = ?, priority = ?, updated_ts = ? WHERE id = ?",
		s.Title, s.Notes, s.Due, s.RemindAt, s.Priority, now, id)
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

func Delete(ctx context.Context, q db.Querier, id int64) (bool, error) {
	res, err := q.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func IDString(id int64) string { return strconv.FormatInt(id, 10) }
