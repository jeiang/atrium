package settings

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

type commitKey struct{}

type Commit interface {
	Lock() (unlock func())
	Prepare(ctx context.Context, q db.Querier) (publish func(), err error)
}

func WithCommit(ctx context.Context, c Commit) context.Context {
	return context.WithValue(ctx, commitKey{}, c)
}

func WithTx(ctx context.Context, d *db.DB, fn func(*sql.Tx) error) error {
	c, ok := ctx.Value(commitKey{}).(Commit)
	if !ok {
		return d.WithTx(ctx, fn)
	}
	unlock := c.Lock()
	defer unlock()
	var publish func()
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		var err error
		publish, err = c.Prepare(ctx, tx)
		return err
	})
	if err == nil && publish != nil {
		publish()
	}
	return err
}
