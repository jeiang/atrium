package main

import (
	"context"
	"os"

	"github.com/jeiang/atrium/internal/assistant/assistantmodule"
	"github.com/jeiang/atrium/internal/assistant/tools/grafana/grafanamodule"
	"github.com/jeiang/atrium/internal/calendar/calendarmodule"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailmodule"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/media/mediamodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/money/moneymodule"
	"github.com/jeiang/atrium/internal/notify/notifymodule"
	"github.com/jeiang/atrium/internal/notion/notionmodule"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

var version = "dev"

func main() {
	ctx, stop := cli.SignalContext(context.Background())
	code := newApp().Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func newApp() *cli.App {
	app := cli.New("atrium", logx.Setup)
	app.Version = version
	app.Add(commands(imapclient.Opener, nil)...)
	return app
}

func newSet(clients run.Clients, now func() float64) *module.Set {
	var set *module.Set
	guard := func(ctx context.Context, q db.Querier, key, value string) error {
		return set.Guard(ctx, q, key, value)
	}
	set, err := module.NewSet(settings.Default, config.OS,
		coremodule.Module{Version: version, Guard: guard},
		mailmodule.Module{Clients: clients, Now: now},
		calendarmodule.Module{},
		notionmodule.Module{},
		moneymodule.Module{Now: now},
		mediamodule.Module{Now: now},
		assistantmodule.Module{},
		notifymodule.Module{},
		grafanamodule.Module{},
	)
	if err != nil {
		panic(err)
	}
	return set
}

func commands(clients run.Clients, now func() float64) []*cli.Command {
	set := newSet(clients, now)
	cmds := append(set.Commands(), web.ServeCommand(set.Pages()...), codemodeWorkerCommand())
	return pinned(cmds)
}
