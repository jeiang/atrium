package librarymodule

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/apps/library"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

func clock(now func() float64) func() float64 {
	if now == nil {
		return library.Now
	}
	return now
}

func pageService(now func() float64, torrents contract.Provider) func(*web.Deps) *mediaapp.Service {
	return func(d *web.Deps) *mediaapp.Service {
		now := clock(now)
		effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, now)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(effects.Drain)
		links := library.NewLinks(d.Settings, d.DB, d.Cfg.MediaLinkKeyPath())
		return mediaapp.New(mediaapp.Deps{DB: d.DB, Env: d.Env, Ledger: effects, Now: now, CacheDir: d.Cfg.MediaCacheDir(), Links: links, Torrents: torrents})
	}
}

func openService(ctx context.Context, d *db.DB, env config.Env, cfg config.Config, now func() float64, torrents contract.Provider) (*mediaapp.Service, error) {
	effects, err := module.SetFrom(ctx).Catalog().Ledger(d, env, cfg, now)
	if err != nil {
		return nil, err
	}
	return mediaapp.New(mediaapp.Deps{DB: d, Env: env, Ledger: effects, Now: now, CacheDir: cfg.MediaCacheDir(), Torrents: torrents}), nil
}

func publicCheck(cfg config.Config, _ config.Env, d *db.DB, reg *settings.Registry) func(*http.Request) bool {
	return library.NewLinks(reg, d, cfg.MediaLinkKeyPath()).Verifier()
}
