package serverops

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

func ReviewFindings(ctx context.Context, q db.Querier, limit int) ([]FindingRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, fact, severity, title, body, created_at FROM ops_findings WHERE resolved_at IS NULL "+
			"ORDER BY created_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (FindingRow, error) {
		var f FindingRow
		err := r.Scan(&f.ID, &f.Fact, &f.Severity, &f.Title, &f.Body, &f.CreatedAt)
		return f, err
	})
}
