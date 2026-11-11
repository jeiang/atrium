package proposals

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

type DismissedProposal struct {
	ID    int64
	Kind  string
	Title string
}

func DismissedSince(ctx context.Context, q db.Querier, from float64, limit int) ([]DismissedProposal, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, kind, title FROM proposals WHERE status = 'dismissed' AND decided_ts >= ? ORDER BY id DESC LIMIT ?", from, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (DismissedProposal, error) {
		var d DismissedProposal
		err := r.Scan(&d.ID, &d.Kind, &d.Title)
		return d, err
	})
}

func PayloadThread(ctx context.Context, q db.Querier, id int64) (int64, bool, error) {
	var thread int64
	err := q.QueryRowContext(ctx, "SELECT COALESCE(json_extract(payload, '$.thread_id'), 0) FROM proposals WHERE id = ? AND kind = 'permission'", id).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return thread, thread != 0, nil
}
