package journal

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

func ContentJSON(ctx context.Context, q db.Querier, threadID int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT content_json FROM thread_messages WHERE thread_id = ?", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var content string
		err := r.Scan(&content)
		return content, err
	})
}

func MinSeq(ctx context.Context, q db.Querier, threadID, runID int64) (*int64, error) {
	var first sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND run_id = ?", threadID, runID).Scan(&first)
	if err != nil || !first.Valid {
		return nil, err
	}
	return &first.Int64, nil
}
