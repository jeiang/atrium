package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type ReviewOptions struct {
	Application func(*web.Deps) *mailapp.Service
	Clients     func(config.Config) mail.OpenFunc
	Enabled     func(context.Context, db.Querier, string) (bool, error)
	Tools       registry.Source
}

func recoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}

func ReviewServices(o ReviewOptions) func(*web.Deps) reviewpages.Services {
	return func(d *web.Deps) reviewpages.Services {
		open := imapclient.Opener(d.Cfg)
		if o.Clients != nil {
			open = o.Clients(d.Cfg)
		}
		now := mail.WallClock
		syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: now}
		cat := module.SetFrom(d.Ctx).Catalog()
		effects, err := cat.Ledger(d.DB, d.Env, open, now)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(effects.Drain)
		registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: effects, Open: open, Syncer: syncer, Now: now, Env: d.Env, Tools: o.Tools, Approvals: cat.Approvals(), Resume: func(ctx context.Context, runID int64) error {
			if d.ResumeRun == nil {
				return proposals.ProposalError("the assistant module is not running")
			}
			return d.ResumeRun(ctx, runID)
		}})
		if err != nil {
			panic(err)
		}
		liveSvc := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: effects, Open: open, Registry: registry, Now: now}
		draftStore := &drafts.Store{DB: d.DB, Cfg: d.Cfg, Ledger: effects, Open: open, Now: now}
		pipeline := &proposals.Pipeline{DB: d.DB, Ledger: effects, Registry: registry, Now: now, Enabled: o.Enabled}
		var mailService *mailapp.Service
		if o.Application != nil {
			mailService = o.Application(d).WithSender(draftStore)
		}
		return reviewpages.Services{
			Mail:   mailService,
			Review: reviewapp.New(reviewapp.Deps{DB: d.DB, Ledger: effects, Drafts: draftStore, Live: liveSvc, Registry: registry, Pipeline: pipeline}),
		}
	}
}

func ReviewReads(d *web.Deps) *reviewapp.Service {
	return ReviewServices(ReviewOptions{})(d).Review
}
