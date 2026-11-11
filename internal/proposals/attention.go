package proposals

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type PendingProposal struct {
	ID        int64
	Kind      string
	Title     string
	CreatedTS float64
}

func PendingProposals(ctx context.Context, q db.Querier) ([]PendingProposal, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, kind, title, created_ts FROM proposals WHERE status = 'pending' ORDER BY created_ts")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (PendingProposal, error) {
		var p PendingProposal
		err := r.Scan(&p.ID, &p.Kind, &p.Title, &p.CreatedTS)
		return p, err
	})
}
