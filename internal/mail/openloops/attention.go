package openloops

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type OpenCommitment struct {
	ID        int64
	Text      string
	Due       string
	CreatedTS float64
}

func AttentionCommitments(ctx context.Context, q db.Querier) ([]OpenCommitment, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, text, due, created_ts FROM commitments WHERE owner = 'me' AND status = 'open' AND due IS NOT NULL ORDER BY due")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (OpenCommitment, error) {
		var c OpenCommitment
		err := r.Scan(&c.ID, &c.Text, &c.Due, &c.CreatedTS)
		return c, err
	})
}

type OpenLoop struct {
	ThreadKey   string
	State       string
	Subject     string
	Counterpart string
	Since       float64
}

func AttentionLoops(ctx context.Context, q db.Querier, now float64) ([]OpenLoop, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT l.thread_key, l.state, l.subject, l.counterpart, l.since FROM open_loops l "+
			"LEFT JOIN loop_overrides o ON o.thread_key = l.thread_key "+
			"WHERE COALESCE(o.dismissed, 0) = 0 AND (o.snoozed_until IS NULL OR o.snoozed_until <= ?) ORDER BY l.since", now)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (OpenLoop, error) {
		var l OpenLoop
		err := r.Scan(&l.ThreadKey, &l.State, &l.Subject, &l.Counterpart, &l.Since)
		return l, err
	})
}
