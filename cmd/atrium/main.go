package main

import (
	"context"
	"os"

	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/notify"
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

// commands are the subcommands in the order cli.py's parser lists them; now is the clock of the commands that
// take one (the wall clock when nil).
func commands(clients run.Clients, now func() float64) []*cli.Command {
	mailCmds := mailcmd.Options{Clients: clients, Now: now}
	return []*cli.Command{
		run.SyncCommand(clients),
		run.WatchCommand(clients),
		mailCmds.RulesCommand(),
		mailCmds.ShadowReportCommand(),
		mailCmds.ReindexCommand(),
		mailCmds.RebalanceCommand(),
		mailCmds.DeletionsCommand(),
		mailCmds.GmailLabelsCommand(),
		mailCmds.LiveCommand(),
		mailCmds.ProposalsCommand(),
		openloops.Command(now),
		watches.Command(now),
		agentcmd.Command(),
		mediacmd.Command(now),
		web.ServeCommand(
			web.Page{Module: chatpages.Module(chatpages.Options{}), Templates: chatpages.Templates()},
			web.Page{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Globals: pages.Globals()},
			web.Page{Module: notify.Module, Templates: notify.Templates()},
			web.Page{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers()}), Templates: reviewpages.Templates()},
		),
		codemodeWorkerCommand(),
	}
}
