package mailmodule

import (
	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/applier"
	"github.com/jeiang/atrium/internal/apps/mail/live"
	mailsync "github.com/jeiang/atrium/internal/apps/mail/sync"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/review"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

func (m Module) pageEffects(d *web.Deps) (*mailapp.Service, *reviewapp.Service) {
	open := m.opener(d.Cfg)
	syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: mail.WallClock}
	cat := module.SetFrom(d.Ctx).Catalog()
	store, err := cat.Ledger(d.DB, d.Env, d.Cfg, mail.WallClock)
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(store.Drain)
	registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: store, Env: d.Env, Cfg: d.Cfg, Now: mail.WallClock, Approvals: cat.Approvals()})
	if err != nil {
		panic(err)
	}
	l := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: store, Open: open, Registry: registry, Now: mail.WallClock}
	return mailPageService(d).WithApplier(applier.New(l, open, syncer)), reviewapp.New(reviewapp.Deps{DB: d.DB, Kinds: []review.Contribution{{Module: unit.Mail, Category: unit.Mail, Kinds: review.Kinds{Actions: l.ReviewActions()}}}})
}
