package proposals

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func CountPending(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposals WHERE status = 'pending'").Scan(&n)
	return n, err
}
