package mailmodule

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/accountpages"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

func init() {
	for _, e := range []struct {
		kind, label, url, key string
		fields                []integrations.Field
	}{
		{endpoint.EndpointChat, "Mail chat endpoint", endpoint.KeyChatURL, endpoint.KeyChatKey,
			[]integrations.Field{{Key: endpoint.KeyChatModel, Label: "Model"}}},
		{endpoint.EndpointEmbed, "Mail embed endpoint", endpoint.KeyEmbedURL, endpoint.KeyEmbedKey, nil},
	} {
		integrations.Register(integrations.Integration{
			Name: endpoint.Integration(e.kind), Label: e.label, Module: module.Mail, URLKey: e.url, SecretKey: e.key,
			Help:   "OpenAI-compatible base URL ending in /v1; the Bearer key is optional.",
			Fields: e.fields,
			Test:   probeTest(e.kind),
		})
	}
}

func probeTest(kind string) func(context.Context, integrations.Conn) (string, error) {
	return func(ctx context.Context, c integrations.Conn) (string, error) {
		res, err := mail.ProbeCandidate(ctx, c.DB, kind, c.URL, c.Secret, c.HTTP, mail.WallClock())
		if err != nil {
			return "", err
		}
		if !res.OK {
			return "", errors.New(res.Reason)
		}
		detail := fmt.Sprintf("%d models listed", len(res.Models))
		if res.Dimension > 0 {
			detail += fmt.Sprintf(", %d dimensions", res.Dimension)
		}
		if res.Hosted {
			detail += "; hosted endpoint: mail content leaves this host"
		}
		return detail, nil
	}
}

type Module struct {
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
}

func (m Module) Register(mc module.Context) module.Registration {
	mailCmds := mailcmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now}
	cmds := []*cli.Command{
		run.SyncCommand(mc.Env, m.Clients),
		run.WatchCommand(mc.Env, m.Clients),
		mailCmds.RulesCommand(),
		mailCmds.ShadowReportCommand(),
		mailCmds.ReindexCommand(),
		mailCmds.LiveCommand(),
		mailCmds.EndpointsCommand(),
	}
	cmds = append(cmds, mailCmds.ScanCommands()...)
	cmds = append(cmds, mailCmds.RefreshCommands()...)
	cmds = append(cmds, mailCmds.TrackingCommands()...)
	return module.Registration{
		Name:      module.Mail,
		Attention: []attention.Source{attentionsources.Drafts(), attentionsources.Loops(), attentionsources.Commitments(), attentionsources.Packages()},
		Requires: []module.Requirement{
			module.Setting(endpoint.KeyChatURL),
			module.Setting(endpoint.KeyEmbedURL),
			{Name: "endpoint probes", Check: func(ctx context.Context, p module.Probe) (string, error) {
				return mail.ProbeFailures(ctx, p.Q)
			}},
		},
		Pages: []web.Page{
			{Module: pages.Module, Templates: pages.Templates(), Globals: pages.Globals(), Nav: []web.NavItem{
				{Section: "review", Label: "Mail decisions", URL: "/triage", Icon: "route", Page: "triage", Order: 40},
				{Section: "library", Label: "Mail rules", URL: "/rules", Icon: "scroll-text", Page: "rules", Order: 30},
				{Label: "Mail accounts", URL: "/settings/mail", Page: "mail", Settings: true, Order: 20},
			}},
			{Module: accountpages.Module, Templates: accountpages.Templates()},
		},
		Commands: cmds,
		Leaves: map[string]module.Leaf{
			"live pause":      {Always: true},
			"live resume":     {Always: true},
			"live status":     {Always: true},
			"endpoints probe": {Always: true},
		},
		Tools: func(r *registry.Registry) error {
			mailtools.RegisterProposalKinds()
			return r.Add(mailtools.Tools()...)
		},
	}
}
