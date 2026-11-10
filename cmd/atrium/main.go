package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/assistant/assistantmodule"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/tools/grafana/grafanamodule"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/calendar/calendarmodule"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/db"
	kbcallables "github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/mailmodule"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/media/mediamodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notify/notifymodule"
	"github.com/jeiang/atrium/internal/notion/notionmodule"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/sdk"
)

var version = "dev"

func main() {
	ctx, stop := cli.SignalContext(context.Background())
	reg := settings.New()
	code := newApp(config.OS, reg, config.NewState(reg), sdk.Modules()).Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func newApp(env config.Env, reg *settings.Registry, st *config.State, mods []module.Module) *cli.App {
	return newAppWith(env, reg, st, mods, imapclient.Opener, nil, logx.Setup)
}

func newAppWith(env config.Env, reg *settings.Registry, st *config.State, mods []module.Module, clients func(config.Config) mail.OpenFunc, now func() float64, setup func(io.Writer)) *cli.App {
	set := newSet(env, reg, st, mods, clients, now)
	app := cli.New("atrium", setup)
	app.Version = version
	app.Env = env
	app.Settings = reg
	app.State = st
	app.SetupContext = func(ctx context.Context) context.Context { return module.WithSet(ctx, set) }
	app.Bootstrap = bootstrap(env, reg)
	app.Add(append(set.Commands(), web.ServeCommand(set, set.Pages()...))...)
	return app
}

func newSet(env config.Env, reg *settings.Registry, st *config.State, mods []module.Module, clients func(config.Config) mail.OpenFunc, now func() float64) *module.Set {
	var set *module.Set
	guard := func(ctx context.Context, q db.Querier, key, value string) error {
		return set.Guard(ctx, q, key, value)
	}
	all := append([]module.Module{
		coremodule.Module{Version: version, Guard: guard, Clients: clients, Now: now},
		contactsmodule.Module{},
		mailmodule.Module{Clients: clients, Now: now},
		calendarmodule.Module{},
		notionmodule.Module{Env: env},
		mediamodule.Module{Now: now},
		assistantmodule.Module{Callables: []map[string]worker.Callable{calendarcmd.Callables(), mediacmd.Callables(), mailcmd.Callables(), kbcallables.Callables()}},
		notifymodule.Module{Budget: budgetStatus},
		grafanamodule.Module{},
	}, mods...)
	set, err := module.NewSet(reg, st, env, all...)
	if err != nil {
		panic(err)
	}
	return set
}

func budgetStatus(ctx context.Context, q db.Querier, now float64, loc *time.Location) (notify.Budget, error) {
	st, err := budget.StatusAt(ctx, q, now, loc)
	return notify.Budget{SpentUSD: st.SpentUSD, BudgetUSD: st.BudgetUSD, Share: st.Share}, err
}
