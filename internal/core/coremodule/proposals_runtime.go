package coremodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
	"github.com/jeiang/atrium/internal/proposals/proposalscmd"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

type proposalsDeps struct {
	Now     func() float64
	Env     config.Env
	Enabled func(context.Context, db.Querier, string) (bool, error)
	Store   func(*db.DB, config.Config) *ledger.Store
	Tools   registry.Source
}

func (o proposalsDeps) session(ctx context.Context, run func(proposalscmd.Runtime) error) error {
	env := o.Env
	if env == nil {
		env = config.OS
	}
	cfg, err := config.Load(env)
	if err != nil {
		return err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := config.Boot(ctx, env, d, cfg); err != nil {
		return err
	}
	if set := module.SetFrom(ctx); set != nil {
		if err := set.Refresh(ctx, d.Read); err != nil {
			return err
		}
	}
	rt := proposalscmd.Runtime{
		Reads: reviewapp.New(reviewapp.Deps{DB: d}),
		Review: func() *reviewapp.Service {
			store := o.Store(d, cfg)
			cat := module.SetFrom(ctx).Catalog()
			registry, err := cat.Proposals(module.EffectDeps{
				DB: d, Ledger: store, Cfg: cfg, Now: o.Now, Env: env, Tools: o.Tools, Approvals: cat.Approvals(),
			})
			if err != nil {
				panic(err)
			}
			p := &proposals.Pipeline{
				DB: d, Ledger: store, Registry: registry, Now: o.Now, Enabled: o.Enabled,
			}
			return reviewapp.New(reviewapp.Deps{DB: d, Ledger: p.Ledger, Registry: p.Registry, Pipeline: p})
		},
		Scan: func(scanCtx context.Context, now func() float64) ([]proposalsapp.ScanResult, bool, error) {
			var out []proposalsapp.ScanResult
			ran := false
			for _, s := range module.SetFrom(ctx).Catalog().ProposalScans() {
				results, ok, err := s.Run(scanCtx, d, cfg, now)
				if err != nil {
					return nil, true, err
				}
				if ok {
					ran = true
					out = append(out, results...)
				}
			}
			return out, ran, nil
		},
	}
	return run(rt)
}
