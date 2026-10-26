package mailmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/accountpages"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Clients run.Clients
	Now     func() float64
}

func (m Module) Register(module.Context) module.Registration {
	mailCmds := mailcmd.Options{Clients: m.Clients, Now: m.Now}
	return module.Registration{
		Name: module.Mail,
		Soft: []string{module.Contacts},
		Requires: []module.Requirement{
			module.Setting(mail.KeyChatURL),
			module.Setting(mail.KeyEmbedURL),
			{Name: "endpoint probes", Check: func(ctx context.Context, p module.Probe) (string, error) {
				return mail.ProbeFailures(ctx, p.Q)
			}},
		},
		Pages: []web.Page{
			{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Globals: pages.Globals()},
			{Module: accountpages.Module, Templates: accountpages.Templates()},
		},
		Commands: []*cli.Command{
			run.SyncCommand(m.Clients),
			run.WatchCommand(m.Clients),
			mailCmds.RulesCommand(),
			mailCmds.ShadowReportCommand(),
			mailCmds.ReindexCommand(),
			mailCmds.RebalanceCommand(),
			mailCmds.DeletionsCommand(),
			mailCmds.GmailLabelsCommand(),
			mailCmds.LiveCommand(),
			mailCmds.ProposalsCommand(),
			openloops.Command(m.Now),
			watches.Command(m.Now),
			mailCmds.EndpointsCommand(),
		},
		Leaves: map[string]module.Leaf{
			"live pause":      {Always: true},
			"live resume":     {Always: true},
			"live status":     {Always: true},
			"endpoints probe": {Always: true},
		},
		Tools: func(r *registry.Registry) error { return r.Add(mailtools.Tools()...) },
	}
}
