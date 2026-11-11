package embed

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func PendingTotal(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COALESCE(SUM(total - done), 0) FROM embed_index").Scan(&n)
	return n, err
}
