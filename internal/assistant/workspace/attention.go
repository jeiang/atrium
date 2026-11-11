package workspace

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type PlanRef struct {
	ThreadID  int64
	Title     string
	UpdatedAt float64
}

func Plans(ctx context.Context, q db.Querier) ([]PlanRef, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT p.thread_id, COALESCE(t.title, ''), p.updated_at FROM thread_plans p LEFT JOIN threads t ON t.id = p.thread_id ORDER BY p.updated_at")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (PlanRef, error) {
		var p PlanRef
		err := r.Scan(&p.ThreadID, &p.Title, &p.UpdatedAt)
		return p, err
	})
}
