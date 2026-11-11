package embedding

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Message struct {
	ID      int64
	Account string
	Subject string
	Name    string
	Addr    string
	Body    string
}

func Pending(ctx context.Context, q db.Querier, where string, limit int) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, f.body FROM messages m "+
			"JOIN message_fts f ON f.rowid = m.id WHERE "+where+" AND m.gone_at IS NULL ORDER BY m.id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		var subject, name, addr, body sql.NullString
		err := r.Scan(&m.ID, &m.Account, &subject, &name, &addr, &body)
		m.Subject, m.Name, m.Addr, m.Body = subject.String, name.String, addr.String, body.String
		return m, err
	})
}

func Mark(ctx context.Context, q db.Querier, id int64) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded = 1 WHERE id = ?", id)
	return err
}

func Reset(ctx context.Context, q db.Querier) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded = 0")
	return err
}

func MarkIn(ctx context.Context, q db.Querier, table string) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded = 1 WHERE id IN (SELECT rowid FROM "+table+")")
	return err
}

func Counts(ctx context.Context, q db.Querier, table string) (done, total int, err error) {
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts) AND id IN (SELECT rowid FROM "+table+")").Scan(&done); err != nil {
		return 0, 0, err
	}
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND id IN (SELECT rowid FROM message_fts)").Scan(&total); err != nil {
		return 0, 0, err
	}
	return done, total, nil
}
