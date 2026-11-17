package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func recoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}
