package drafts

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type AttentionDraft struct {
	ID        int64
	Subject   string
	To        string
	UpdatedAt float64
}

func AttentionDrafts(ctx context.Context, q db.Querier) ([]AttentionDraft, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, subject, to_addrs, updated_at FROM drafts WHERE status = 'draft' ORDER BY updated_at")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (AttentionDraft, error) {
		var d AttentionDraft
		err := r.Scan(&d.ID, &d.Subject, &d.To, &d.UpdatedAt)
		return d, err
	})
}
