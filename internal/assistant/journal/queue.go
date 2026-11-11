package journal

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func QueueDepth(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_queue WHERE consumed_at IS NULL").Scan(&n)
	return n, err
}
