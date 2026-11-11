package tasks

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func Restore(ctx context.Context, q db.Querier, t Task) (bool, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE tasks SET title = ?, notes = ?, due = ?, remind_at = ?, status = ?, priority = ?, updated_ts = ?, done_ts = ? WHERE id = ?",
		t.Title, t.Notes, t.Due, t.RemindAt, t.Status, t.Priority, t.UpdatedTS, t.DoneTS, t.ID)
	return db.Changed(res, err)
}
