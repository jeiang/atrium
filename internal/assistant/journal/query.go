package journal

import (
	"context"
	"database/sql"
	"errors"

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

func LatestUserContentMatching(ctx context.Context, q db.Querier, threadID int64, like string) (string, error) {
	var content string
	err := q.QueryRowContext(ctx,
		"SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' AND superseded = 0 "+
			"AND content_json LIKE ? ORDER BY seq DESC LIMIT 1", threadID, like).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}

const threadRefs = "SELECT m.thread_id AS thread_id, m.id AS message_id, CASE WHEN b.type = 'object' THEN " +
	"CASE WHEN json_extract(b.value, '$.type') = 'tool_result' AND NOT COALESCE(json_extract(b.value, '$.is_error'), 0) " +
	"AND json_valid(json_extract(b.value, '$.content')) THEN json_extract(json_extract(b.value, '$.content'), '$.proposal_id') END " +
	"END AS proposal_id FROM thread_messages m, json_each(m.content_json) b WHERE m.content_json LIKE '%proposal_id%'"

func ThreadRefsSQL() string { return threadRefs }

func ProposalThread(ctx context.Context, q db.Querier, id int64) (int64, bool, error) {
	var thread int64
	err := q.QueryRowContext(ctx, "SELECT thread_id FROM ("+threadRefs+") WHERE proposal_id = ? ORDER BY message_id LIMIT 1", id).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return thread, err == nil, err
}
