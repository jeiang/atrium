package readpaths_test

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/readpaths"
	"github.com/jeiang/atrium/internal/mail/live"
)

func init() {
	readpaths.Register(readpaths.Path{
		Name:   "mail.live.Status",
		Golden: "live.status",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			cfg, err := config.Load(config.OS)
			if err != nil {
				return nil, err
			}
			// make_db.py's config runs in live mode.
			cfg.Mode = config.Live
			return (&live.Live{DB: d, Cfg: cfg}).Status(ctx)
		},
	})
	readpaths.Register(readpaths.Path{
		Name:   "mail.live.List",
		Golden: "live.list_actions",
		Run: func(ctx context.Context, d *db.DB) (any, error) {
			return live.List(ctx, d.Read, "", "", live.DefaultLimit, 0)
		},
	})
}
