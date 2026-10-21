package coremodule

import (
	"github.com/jeiang/atrium/internal/about"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbcmd"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/secrets/secretscmd"
	"github.com/jeiang/atrium/internal/settings/settingscmd"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Version string
}

func (m Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name: module.Core,
		Core: true,
		Pages: []web.Page{
			{Module: about.Module(m.Version), Templates: about.Templates()},
			{Module: reviewpages.Module(reviewpages.Options{Writes: toolset.Undoers(config.OS)}), Templates: reviewpages.Templates()},
		},
		Commands: []*cli.Command{
			settingscmd.Command(),
			secretscmd.Command(),
			dbcmd.Command(),
		},
	}
}
