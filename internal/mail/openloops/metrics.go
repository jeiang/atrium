package openloops

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func CountOpen(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM open_loops").Scan(&n)
	return n, err
}
