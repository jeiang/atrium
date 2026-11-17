package drafts

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Need struct {
	ID        int64
	Subject   string
	To        string
	Status    Status
	CreatedAt float64
}

func Needs(ctx context.Context, q db.Querier) ([]Need, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, subject, to_addrs, status, created_at FROM drafts WHERE status IN ('draft', 'unknown')")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Need, error) {
		var n Need
		err := r.Scan(&n.ID, &n.Subject, &n.To, &n.Status, &n.CreatedAt)
		return n, err
	})
}
