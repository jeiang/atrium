package drafts

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func CountDraft(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM drafts WHERE status = 'draft'").Scan(&n)
	return n, err
}
