package tasks

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

func Restore(ctx context.Context, q db.Querier, t Task) (bool, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE tasks SET title = ?, notes = ?, due = ?, remind_at = ?, status = ?, priority = ?, source_kind = ?, source_id = ?, updated_ts = ?, done_ts = ? WHERE id = ?",
		t.Title, t.Notes, t.Due, t.RemindAt, t.Status, t.Priority, t.SourceKind, t.SourceID, t.UpdatedTS, t.DoneTS, t.ID)
	return db.Changed(res, err)
}

type Filter struct {
	DueFrom  string
	DueTo    string
	LinkKind string
	LinkID   string
	Text     string
}

type PageKey struct {
	Rank      int     `json:"r"`
	NoDue     int     `json:"n"`
	Due       string  `json:"d"`
	CreatedTS float64 `json:"c"`
	ID        int64   `json:"id"`
}

type Counts struct {
	Open      int `json:"open"`
	Done      int `json:"done"`
	Dismissed int `json:"dismissed"`
}

const (
	rankExpr  = "CASE status WHEN 'open' THEN 0 WHEN 'done' THEN 1 ELSE 2 END"
	noDueExpr = "CASE WHEN COALESCE(due, '') = '' THEN 1 ELSE 0 END"
)

func PageKeyOf(t Task) PageKey {
	k := PageKey{NoDue: 1, CreatedTS: t.CreatedTS, ID: t.ID}
	switch t.Status {
	case StatusDone:
		k.Rank = 1
	case StatusDismissed:
		k.Rank = 2
	}
	if t.Due != nil && *t.Due != "" {
		k.NoDue, k.Due = 0, *t.Due
	}
	return k
}

func (f Filter) where() (string, []any) {
	var clauses []string
	var args []any
	if f.DueFrom != "" {
		clauses = append(clauses, "COALESCE(due, '') <> '' AND substr(due, 1, 10) >= ?")
		args = append(args, f.DueFrom)
	}
	if f.DueTo != "" {
		clauses = append(clauses, "COALESCE(due, '') <> '' AND substr(due, 1, 10) <= ?")
		args = append(args, f.DueTo)
	}
	if f.LinkKind != "" {
		clauses = append(clauses, "source_kind = ? AND source_id = ?")
		args = append(args, f.LinkKind, f.LinkID)
	}
	if text := strings.TrimSpace(f.Text); text != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(text)) + "%"
		clauses = append(clauses, `(lower(title) LIKE ? ESCAPE '\' OR lower(notes) LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	if len(clauses) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(clauses, " AND "), args
}

func Page(ctx context.Context, q db.Querier, f Filter, status string, limit int, after *PageKey) ([]Task, error) {
	where, args := f.where()
	query := "SELECT " + columns + " FROM tasks WHERE " + where
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	order := "(" + rankExpr + ", " + noDueExpr + ", COALESCE(due, ''), created_ts, id)"
	if after != nil {
		query += " AND " + order + " > (?, ?, ?, ?, ?)"
		args = append(args, after.Rank, after.NoDue, after.Due, after.CreatedTS, after.ID)
	}
	query += " ORDER BY " + rankExpr + ", " + noDueExpr + ", COALESCE(due, ''), created_ts, id LIMIT ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Task, error) { return scan(r) })
}

func CountByStatus(ctx context.Context, q db.Querier, f Filter) (Counts, error) {
	where, args := f.where()
	var c Counts
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(status = 'open'), 0), COALESCE(SUM(status = 'done'), 0), COALESCE(SUM(status = 'dismissed'), 0) FROM tasks WHERE "+where,
		args...).Scan(&c.Open, &c.Done, &c.Dismissed)
	return c, err
}
