package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/web"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := newApp().Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func newApp() *cli.App {
	app := cli.New("atrium", logx.Setup)
	app.Version = version
	app.Add(web.ServeCommand(
		web.Page{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Globals: pages.Globals()},
		web.Page{Module: notify.Module, Templates: notify.Templates()},
		web.Page{Module: reviewpages.Module(reviewpages.Options{}), Templates: reviewpages.Templates()},
	))
	return app
}
