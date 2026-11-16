package hub

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
)

type ComposeOptions struct {
	Tools   registry.Source
	Enabled func(context.Context, db.Querier, string) (bool, error)
}

type Runtime struct {
	Review *reviewapp.Service
	Ledger *ledger.Store
	Now    func() float64
}

func Now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Compose(o ComposeOptions) func(*web.Deps) Runtime {
	return func(d *web.Deps) Runtime {
		cat := module.SetFrom(d.Ctx).Catalog()
		effects, err := cat.Ledger(d.DB, d.Env, d.Cfg, Now)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(effects.Drain)
		registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: effects, Cfg: d.Cfg, Now: Now, Env: d.Env, Tools: o.Tools, Approvals: cat.Approvals(), Resume: func(ctx context.Context, runID int64) error {
			if d.ResumeRun == nil {
				return proposals.ProposalError("the assistant module is not running")
			}
			return d.ResumeRun(ctx, runID)
		}})
		if err != nil {
			panic(err)
		}
		kinds := cat.ReviewKinds(module.ReviewDeps{DB: d.DB, Ledger: effects, Registry: registry, Env: d.Env, Cfg: d.Cfg, Now: Now})
		pipeline := &proposals.Pipeline{DB: d.DB, Ledger: effects, Registry: registry, Now: Now, Enabled: o.Enabled}
		return Runtime{
			Review: reviewapp.New(reviewapp.Deps{DB: d.DB, Ledger: effects, Kinds: kinds, Registry: registry, Pipeline: pipeline, Modules: module.SetFrom(d.Ctx)}),
			Ledger: effects,
			Now:    Now,
		}
	}
}

func Reads(d *web.Deps) *reviewapp.Service { return Compose(ComposeOptions{})(d).Review }
