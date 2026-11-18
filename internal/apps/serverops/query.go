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
	return collectSamples(q.QueryContext(ctx, query, params...))
}

func LatestSamples(ctx context.Context, q db.Querier) ([]SampleRow, error) {
	return collectSamples(q.QueryContext(ctx,
		"SELECT s.ts, s.host, s.metric, s.value, s.state, s.detail FROM ops_samples s "+
			"JOIN (SELECT host, MAX(ts) AS ts FROM ops_samples GROUP BY host) l ON l.host = s.host AND l.ts = s.ts "+
			"ORDER BY s.host, s.metric, s.id DESC"))
}

func collectSamples(rows *sql.Rows, err error) ([]SampleRow, error) {
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (SampleRow, error) {
		var s SampleRow
		err := r.Scan(&s.TS, &s.Host, &s.Metric, &s.Value, &s.State, &s.Detail)
		return s, err
	})
}
