package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/app"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/web"
)

var version = "dev"

func main() {
	ctx, stop := cli.SignalContext(context.Background())
	code := newApp(config.OS).Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func newApp(env config.Env) *cli.App {
	return newAppWith(env, imapclient.Opener, nil, logx.Setup)
}

func newAppWith(env config.Env, clients func(config.Config) mail.OpenFunc, now func() float64, setup func(io.Writer)) *cli.App {
	var set *module.Set
	var cat *module.Catalog
	mc := module.Context{
		Current: func() *module.Snapshot {
			if set == nil {
				return nil
			}
			return set.Current()
		},
		Evaluate: func(ctx context.Context, q db.Querier) (*module.Snapshot, error) {
			if set == nil {
				return nil, module.ErrNotEvaluated
			}
			return set.Evaluate(ctx, q)
		},
		Catalog: func() *module.Catalog { return cat },
		Env:     env,
	}
	guard := func(ctx context.Context, q db.Querier, key, value string) error {
		return set.Guard(ctx, q, key, value)
	}
	cat, err := app.Catalog(app.Dependencies{
		Version: version,
		Guard:   guard,
		Clients: clients,
		Now:     now,
		Env:     env,
		Budget:  budgetStatus,
	}, mc)
	if err != nil {
		panic(err)
	}
	reg := cat.Settings()
	st := config.NewState(reg)
	st.SetConnectionReconciler(connections.Reconcile)
	st.SetAccountCredential(cat.AccountCredential())
	set, err = module.NewSet(cat, reg, env)
	if err != nil {
		panic(err)
	}
	app := cli.New("atrium", setup)
	app.Version = version
	app.Env = env
	app.Settings = reg
	app.State = st
	app.SetupContext = func(ctx context.Context) context.Context { return cat.Context(module.WithSet(ctx, set)) }
	app.Bootstrap = bootstrap(env, reg)
	app.Add(append(append(set.Commands(), connections.Command(env)), web.ServeCommand(set, set.Pages()...))...)
	return app
}

func budgetStatus(ctx context.Context, q db.Querier, now float64, loc *time.Location) (notify.Budget, error) {
	st, err := inference.StatusAt(ctx, q, now, loc)
	return notify.Budget{
		SpentUSD: st.SpentUSD, BudgetUSD: st.BudgetUSD, Share: st.Share,
		CommittedUSD: st.CommittedUSD, ReservedUSD: st.ReservedUSD, UnknownUSD: st.UnknownUSD,
		ExpiredUSD: st.ExpiredUSD, InteractiveCeilingUSD: st.InteractiveCeilingUSD,
	}, err
}
