package main

import (
	"context"
	"os"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	chatpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	mediapages "github.com/jeiang/atrium/internal/media/pages"
	"github.com/jeiang/atrium/internal/money"
	moneypages "github.com/jeiang/atrium/internal/money/pages"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notion"
	notionpages "github.com/jeiang/atrium/internal/notion/pages"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
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
		notion.Command(notion.Options{}),
		openloops.Command(now),
		watches.Command(now),
		money.Command(now),
		agentcmd.Command(),
		calendarcmd.Command(),
		mediacmd.Command(now),
		settingscmd.Command(),
		web.ServeCommand(
			web.Page{Module: chatpages.Module(chatpages.Options{}), Templates: chatpages.Templates()},
			web.Page{Module: about.Module(version), Templates: about.Templates()},
			web.Page{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Globals: pages.Globals()},
			web.Page{Module: mediapages.Module(mediapages.Options{}), Templates: mediapages.Templates(), Public: mediapages.PublicCheck},
			web.Page{Module: notify.Module, Templates: notify.Templates()},
			web.Page{Module: notionpages.Module(notionpages.Options{}), Templates: notionpages.Templates()},
			web.Page{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers(config.OS)}), Templates: reviewpages.Templates()},
			web.Page{Module: moneypages.Module(moneypages.Options{}), Templates: moneypages.Templates()},
		),
		codemodeWorkerCommand(),
	}
}
