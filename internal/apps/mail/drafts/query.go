package drafts

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

func ForThread(ctx context.Context, q db.Querier, threadID int64) ([]Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM drafts WHERE thread_id = ? ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) { return scanRow(r) })
}

func DictsForThread(ctx context.Context, q db.Querier, threadID int64) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM drafts WHERE thread_id = ? ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (map[string]any, error) {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(columns))
		for i, name := range columns {
			out[name] = values[i]
		}
		return out, nil
	})
}

func DeleteIfDraft(ctx context.Context, q db.Querier, id int64) (bool, error) {
	res, err := q.ExecContext(ctx, "DELETE FROM drafts WHERE id = ? AND status = 'draft'", id)
	return db.Changed(res, err)
}

type Restore struct {
	ID                 int64
	Account            string
	To                 string
	Cc                 string
	Subject            string
	Body               string
	InReplyToMessageID *int64
	InReplyTo          string
	Refs               string
	UpdatedAt          float64
}

func RestoreDraft(ctx context.Context, q db.Querier, r Restore) (bool, error) {
	res, err := q.ExecContext(ctx,
		"UPDATE drafts SET account = ?, to_addrs = ?, cc_addrs = ?, subject = ?, body = ?, in_reply_to_message_id = ?, in_reply_to = ?, refs = ?, updated_at = ? WHERE id = ? AND status = 'draft'",
		r.Account, r.To, r.Cc, r.Subject, r.Body, r.InReplyToMessageID, r.InReplyTo, r.Refs, r.UpdatedAt, r.ID)
	return db.Changed(res, err)
}
