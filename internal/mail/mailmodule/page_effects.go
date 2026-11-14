package mailmodule

import (
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

func mailPageEffects(d *web.Deps) (*mailapp.Service, *reviewapp.Service) {
	open := imapclient.Opener(d.Cfg)
	syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: mail.WallClock}
	cat := module.SetFrom(d.Ctx).Catalog()
	store, err := cat.Ledger(d.DB, d.Env, open, mail.WallClock)
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(store.Drain)
	registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: store, Env: d.Env, Open: open, Syncer: syncer, Now: mail.WallClock, Approvals: cat.Approvals()})
	if err != nil {
		panic(err)
	}
	l := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: store, Open: open, Registry: registry, Now: mail.WallClock}
	return mailPageService(d).WithApplier(applier.New(l, open, syncer)), reviewapp.New(reviewapp.Deps{DB: d.DB, Live: l})
}
