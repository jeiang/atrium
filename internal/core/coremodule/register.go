package coremodule

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/auth/loginlink"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbcmd"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/logx"
	mailpages "github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/settings/settingspages"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Version string
	Guard   settingscmd.Guard
}

func (m Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name: module.Core,
		Core: true,
		Pages: []web.Page{
			{Module: about.Module(m.Version), Templates: about.Templates()},
			{Module: mailpages.PauseModule},
			{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers(config.OS)}), Templates: reviewpages.Templates()},
			{Module: startOps},
			{Module: integrations.Module, Templates: integrations.Templates()},
			{Module: settingspages.Module, Templates: settingspages.Templates()},
		},
		Commands: []*cli.Command{
			settingscmd.Command(m.Guard),
			loginlink.Command(),
			secretscmd.Command(),
			dbcmd.Command(),
			backup.BackupCommand(),
			backup.RestoreCommand(),
		},
	}
}

var log = logx.Logger("atrium.core")

func startOps(_ chi.Router, d *web.Deps) {
	stop, err := ops.Start(context.Background(), d.DB)
	if err != nil {
		log.Error("ops listener did not start", logx.Err, err.Error())
		return
	}
	d.OnClose(stop)
}
