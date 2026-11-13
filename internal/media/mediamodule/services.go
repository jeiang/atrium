package mediamodule

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

func clock(now func() float64) func() float64 {
	if now == nil {
		return media.Now
	}
	return now
}

func pageService(now func() float64) func(*web.Deps) *mediaapp.Service {
	return func(d *web.Deps) *mediaapp.Service {
		now := clock(now)
		effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, imapclient.Opener(d.Cfg), now)
		if err != nil {
			panic(err)
		}
		d.DrainOnClose(effects.Drain)
		links := media.NewLinks(d.Settings, d.DB, d.Cfg.MediaLinkKeyPath())
		return mediaapp.New(mediaapp.Deps{DB: d.DB, Env: d.Env, Ledger: effects, Now: now, CacheDir: d.Cfg.MediaCacheDir(), Links: links})
	}
}

func openService(ctx context.Context, d *db.DB, env config.Env, cfg config.Config, now func() float64) (*mediaapp.Service, error) {
	effects, err := module.SetFrom(ctx).Catalog().Ledger(d, env, imapclient.Opener(cfg), now)
	if err != nil {
		return nil, err
	}
	return mediaapp.New(mediaapp.Deps{DB: d, Env: env, Ledger: effects, Now: now, CacheDir: cfg.MediaCacheDir()}), nil
}

func publicCheck(cfg config.Config, _ config.Env, d *db.DB, reg *settings.Registry) func(*http.Request) bool {
	return media.NewLinks(reg, d, cfg.MediaLinkKeyPath()).Verifier()
}
