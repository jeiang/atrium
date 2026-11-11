package threads

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

func Titles(ctx context.Context, q db.Querier, ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, "SELECT id, title FROM threads WHERE id IN ("+marks+")", args...)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}

func ProjectOf(ctx context.Context, q db.Querier, threadID int64) (*int64, error) {
	var project *int64
	err := q.QueryRowContext(ctx, "SELECT project_id FROM threads WHERE id = ? AND deleted_at IS NULL", threadID).Scan(&project)
	return project, err
}

func MarksOfID(ctx context.Context, q db.Querier, threadID int64) (tool.Marks, error) {
	var marks int64
	err := q.QueryRowContext(ctx, "SELECT marks FROM threads WHERE id = ?", threadID).Scan(&marks)
	if err != nil {
		return 0, err
	}
	return tool.Marks(marks), nil
}

func IDByKindRole(ctx context.Context, q db.Querier, kind Kind, role string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM threads WHERE kind = ? AND role = ? AND deleted_at IS NULL ORDER BY id DESC LIMIT 1", string(kind), role).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
