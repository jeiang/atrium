package embedding

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Record struct {
	ID           int64
	Title        string
	Aliases      string
	Body         string
	EmbeddedHash string
}

func scan(r *sql.Rows) (Record, error) {
	var k Record
	var hash sql.NullString
	err := r.Scan(&k.ID, &k.Title, &k.Aliases, &k.Body, &hash)
	k.EmbeddedHash = hash.String
	return k, err
}

func Pending(ctx context.Context, q db.Querier, where string, limit int, after int64) ([]Record, error) {
	query := "SELECT id, title, aliases, body, embedded_hash FROM kb_records WHERE retracted_at IS NULL"
	if where != "" {
		query += " AND " + where
	}
	rows, err := q.QueryContext(ctx, query+" AND id > ? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, scan)
}

func Load(ctx context.Context, q db.Querier, id int64) (Record, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, title, aliases, body, embedded_hash FROM kb_records WHERE retracted_at IS NULL AND id = ?", id)
	if err != nil {
		return Record{}, false, err
	}
	found, err := db.Collect(rows, scan)
	if err != nil || len(found) == 0 {
		return Record{}, false, err
	}
	return found[0], true, nil
}

func Mark(ctx context.Context, q db.Querier, id int64, hash string) error {
	_, err := q.ExecContext(ctx, "UPDATE kb_records SET embedded_hash = ? WHERE id = ?", hash, id)
	return err
}

func ClearIn(ctx context.Context, q db.Querier, table string) error {
	_, err := q.ExecContext(ctx, "UPDATE kb_records SET embedded_hash = NULL WHERE id IN (SELECT rowid FROM "+table+")")
	return err
}

func Counts(ctx context.Context, q db.Querier, table string) (done, total int, err error) {
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM kb_records WHERE retracted_at IS NULL AND id IN (SELECT rowid FROM "+table+")").Scan(&done); err != nil {
		return 0, 0, err
	}
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM kb_records WHERE retracted_at IS NULL").Scan(&total); err != nil {
		return 0, 0, err
	}
	return done, total, nil
}
