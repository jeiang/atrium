package counts

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func Undelivered(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE delivered = 0").Scan(&n)
	return n, err
}

func FailingPush(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM push_subscriptions WHERE failure_count >= 3").Scan(&n)
	return n, err
}
