package sync

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type AccountSync struct {
	Name       string
	LastSyncAt float64
}

func AccountSyncTimes(ctx context.Context, q db.Querier) ([]AccountSync, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, last_sync_at FROM accounts WHERE last_sync_at IS NOT NULL ORDER BY name")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (AccountSync, error) {
		var a AccountSync
		err := r.Scan(&a.Name, &a.LastSyncAt)
		return a, err
	})
}
