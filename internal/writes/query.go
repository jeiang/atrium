package writes

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Resolution struct {
	ID         int64
	TS         float64
	Actor      string
	Kind       string
	RowID      int64
	ProposalID *int64
	Outcome    string
}

func ResolutionsSince(ctx context.Context, q db.Querier, limit int) ([]Resolution, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ts, actor, kind, row_id, proposal_id, outcome FROM write_resolutions WHERE source = 'phone' ORDER BY ts DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Resolution, error) {
		var x Resolution
		err := r.Scan(&x.ID, &x.TS, &x.Actor, &x.Kind, &x.RowID, &x.ProposalID, &x.Outcome)
		return x, err
	})
}

type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func RecordResolution(ctx context.Context, x Execer, actor, source string, ts float64, kind string, rowID int64, proposalID *int64, outcome, prior string) error {
	var proposal any
	if proposalID != nil {
		proposal = *proposalID
	}
	_, err := x.ExecContext(ctx,
		"INSERT INTO write_resolutions (ts, actor, source, kind, row_id, proposal_id, outcome, prior_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ts, actor, source, kind, rowID, proposal, outcome, prior)
	return err
}
