package counts

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func Outstanding(ctx context.Context, q db.Querier, now float64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE used_ts IS NULL AND expires_ts > ?", now).Scan(&n)
	return n, err
}

func MintedSince(ctx context.Context, q db.Querier, since float64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE created_ts > ?", since).Scan(&n)
	return n, err
}

func Used(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE used_ts IS NOT NULL").Scan(&n)
	return n, err
}
