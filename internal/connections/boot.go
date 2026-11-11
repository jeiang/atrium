package connections

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Reconcile(ctx context.Context, d *db.DB, host config.Host) error {
	store, err := New(d, host, DefaultRegistry())
	if err != nil {
		return err
	}
	if err := store.ReconcileHost(ctx); err != nil {
		return err
	}
	SetDefault(d.Read, store)
	return nil
}
