package serverops

import (
	"context"
	"database/sql"
	"errors"

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

type FindingRow struct {
	ID             int64
	Fact           string
	Severity       string
	Title          string
	Body           string
	Marks          int64
	CreatedAt      float64
	ResolvedAt     *float64
	ResolvedReason *string
}

func CountUnresolved(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_findings WHERE resolved_at IS NULL").Scan(&n)
	return n, err
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

func FindingsByThread(ctx context.Context, q db.Querier, threadID int64, limit int64) ([]FindingRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, fact, severity, title, body, marks, created_at, resolved_at, resolved_reason FROM ops_findings "+
			"WHERE thread_id = ? ORDER BY created_at DESC, id DESC LIMIT ?", threadID, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (FindingRow, error) {
		var f FindingRow
		err := r.Scan(&f.ID, &f.Fact, &f.Severity, &f.Title, &f.Body, &f.Marks, &f.CreatedAt, &f.ResolvedAt, &f.ResolvedReason)
		return f, err
	})
}

func FindingByID(ctx context.Context, q db.Querier, id int64) (*FindingRow, error) {
	var f FindingRow
	err := q.QueryRowContext(ctx,
		"SELECT id, fact, severity, title, body, marks, created_at, resolved_at, resolved_reason FROM ops_findings WHERE id = ?", id).
		Scan(&f.ID, &f.Fact, &f.Severity, &f.Title, &f.Body, &f.Marks, &f.CreatedAt, &f.ResolvedAt, &f.ResolvedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func LatestSampleTS(ctx context.Context, q db.Querier) (*float64, error) {
	var last *float64
	err := q.QueryRowContext(ctx, "SELECT MAX(ts) FROM ops_samples").Scan(&last)
	return last, err
}

func LatestSamples(ctx context.Context, q db.Querier) ([]SampleRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT host, metric, value, state FROM ops_samples WHERE id IN (SELECT MAX(id) FROM ops_samples GROUP BY host, metric)")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (SampleRow, error) {
		var s SampleRow
		err := r.Scan(&s.Host, &s.Metric, &s.Value, &s.State)
		return s, err
	})
}

func UnresolvedFindings(ctx context.Context, q db.Querier) ([]FindingRow, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, fact, severity, title, created_at FROM ops_findings WHERE resolved_at IS NULL ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (FindingRow, error) {
		var f FindingRow
		err := r.Scan(&f.ID, &f.Fact, &f.Severity, &f.Title, &f.CreatedAt)
		return f, err
	})
}
