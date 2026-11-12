package settings

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type commitKey struct{}
type PrepareCommit func(context.Context, db.Querier) (func(bool), error)

func WithCommit(ctx context.Context, prepare PrepareCommit) context.Context {
	return context.WithValue(ctx, commitKey{}, prepare)
}

func WithTx(ctx context.Context, d *db.DB, fn func(*sql.Tx) error) error {
	var publish func(bool)
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if prepare, ok := ctx.Value(commitKey{}).(PrepareCommit); ok {
			var err error
			publish, err = prepare(ctx, tx)
			return err
		}
		return nil
	})
	if publish != nil {
		publish(err == nil)
	}
	return err
}
