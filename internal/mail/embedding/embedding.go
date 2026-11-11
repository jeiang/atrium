package embedding

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Message struct {
	ID           int64
	Account      string
	Subject      string
	Name         string
	Addr         string
	Body         string
	EmbeddedHash string
}

const columns = "m.id, m.account, m.subject, m.from_name, m.from_addr, f.body, m.embedded_hash"

func scan(r *sql.Rows) (Message, error) {
	var m Message
	var subject, name, addr, body, hash sql.NullString
	err := r.Scan(&m.ID, &m.Account, &subject, &name, &addr, &body, &hash)
	m.Subject, m.Name, m.Addr, m.Body = subject.String, name.String, addr.String, body.String
	m.EmbeddedHash = hash.String
	return m, err
}

func Pending(ctx context.Context, q db.Querier, where string, limit int, after int64) ([]Message, error) {
	query := "SELECT " + columns + " FROM messages m JOIN message_fts f ON f.rowid = m.id WHERE m.gone_at IS NULL"
	if where != "" {
		query += " AND " + where
	}
	rows, err := q.QueryContext(ctx, query+" AND m.id > ? ORDER BY m.id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, scan)
}

func Load(ctx context.Context, q db.Querier, id int64) (Message, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM messages m JOIN message_fts f ON f.rowid = m.id WHERE m.gone_at IS NULL AND m.id = ?", id)
	if err != nil {
		return Message{}, false, err
	}
	found, err := db.Collect(rows, scan)
	if err != nil || len(found) == 0 {
		return Message{}, false, err
	}
	return found[0], true, nil
}

func Mark(ctx context.Context, q db.Querier, id int64, hash string) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded_hash = ? WHERE id = ?", hash, id)
	return err
}

func Reset(ctx context.Context, q db.Querier) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded_hash = NULL")
	return err
}

func ClearIn(ctx context.Context, q db.Querier, table string) error {
	_, err := q.ExecContext(ctx, "UPDATE messages SET embedded_hash = NULL WHERE id IN (SELECT rowid FROM "+table+")")
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
