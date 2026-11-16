package serverops

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/review"
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

func ReviewKindFindings(ctx context.Context, q db.Querier, limit int) ([]review.Finding, error) {
	found, err := ReviewFindings(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]review.Finding, 0, len(found))
	for _, f := range found {
		finding := review.Finding{ID: f.ID, Severity: f.Severity, Title: f.Title, Body: f.Body, Created: f.CreatedAt}
		finding.Host, finding.Metric, _ = strings.Cut(f.Fact, ":")
		out = append(out, finding)
	}
	return out, nil
}
