package serverops

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type SampleRow struct {
	TS     float64
	Host   string
	Metric string
	Value  *float64
	State  *string
	Detail *string
}

func Samples(ctx context.Context, q db.Querier, since float64, host, metric string, limit int) ([]SampleRow, error) {
	query := "SELECT ts, host, metric, value, state, detail FROM ops_samples WHERE ts >= ?"
	params := []any{since}
	if host != "" {
		query += " AND host = ?"
		params = append(params, host)
	}
	if metric != "" {
		query += " AND metric = ?"
		params = append(params, metric)
	}
	query += " ORDER BY ts DESC, id DESC LIMIT ?"
	params = append(params, limit)
	rows, err := q.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (SampleRow, error) {
		var s SampleRow
		err := r.Scan(&s.TS, &s.Host, &s.Metric, &s.Value, &s.State, &s.Detail)
		return s, err
	})
}
