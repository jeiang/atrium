package embedding

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type Record struct {
	ID      int64
	Title   string
	Aliases string
	Body    string
}

func Pending(ctx context.Context, q db.Querier, where string, limit int) ([]Record, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, title, aliases, body FROM kb_records WHERE "+where+" AND retracted_at IS NULL ORDER BY id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Record, error) {
		var k Record
		err := r.Scan(&k.ID, &k.Title, &k.Aliases, &k.Body)
		return k, err
	})
}

func Mark(ctx context.Context, q db.Querier, id int64) error {
	_, err := q.ExecContext(ctx, "UPDATE kb_records SET embedded = 1 WHERE id = ?", id)
	return err
}

func MarkIn(ctx context.Context, q db.Querier, table string) error {
	_, err := q.ExecContext(ctx, "UPDATE kb_records SET embedded = 1 WHERE id IN (SELECT rowid FROM "+table+")")
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
