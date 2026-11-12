package mailmodule

import (
	"context"
	"slices"

	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/accountpages"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/mailconn"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/mail/reviewpages"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Drafts(), attentionsources.Loops(), attentionsources.Commitments(), attentionsources.Packages()}
}

func Requirements() []module.Requirement {
	return []module.Requirement{
		module.Setting(endpoint.KeyChatURL),
		{Name: "service/embedding", Check: func(ctx context.Context, p module.Probe) (string, error) {
			return endpoint.EmbeddingConfigured(ctx, p.Q)
		}},
		{Name: "endpoint probes", Check: func(ctx context.Context, p module.Probe) (string, error) {
			return mail.ProbeFailures(ctx, p.Q)
		}},
	}
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "safety", Title: "Safety", Keys: []string{mail.KeyWriteCap}},
		{Name: "loops", Title: "Open loops", Keys: []string{
			mail.KeyLoopsWaitingAfterDays, mail.KeyLoopsRemindAheadDays, mail.KeyLoopsSnoozeDefaultDays, config.KeySnoozeChoices,
		}},
		{Name: "packages", Title: "Packages", Keys: []string{tracking.KeyDestinationCountry, tracking.KeyDestinationPostcode}},
	}
}

func ToolDefinitions() []module.ToolDefinition {
	return mailtools.Definitions()
}

func (m Module) Declare(mc module.Context) module.Declaration {
	mailCmds := mailcmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now}
	cmds := []*cli.Command{
		run.SyncCommand(mc.Env, m.Clients),
		run.WatchCommand(mc.Env, m.Clients),
		mailCmds.RulesCommand(),
		mailCmds.ShadowReportCommand(),
		mailCmds.ReindexCommand(),
		mailCmds.LiveCommand(),
		mailCmds.EndpointsCommand(),
		mailCmds.ReplayCommand(),
	}
	cmds = append(cmds, mailCmds.ScanCommands()...)
	cmds = append(cmds, mailCmds.RefreshCommands()...)
	cmds = append(cmds, mailCmds.TrackingCommands()...)
	return module.Declaration{
		Name:      module.Mail,
		Settings:  slices.Concat(mail.Settings(), endpoint.SettingsDefs(), tracking.SettingsDefs()),
		Host:      mail.HostSettings(),
		Attention: AttentionSources(),
		Requires:  Requirements(),
		Pages: []web.Page{
			{Module: pages.Module, Templates: pages.Templates(), Globals: pages.Globals(), Nav: []web.NavItem{
				{Section: "review", Label: "Mail decisions", URL: "/triage", Icon: "route", Page: "triage", Order: 40},
				{Section: "library", Label: "Mail rules", URL: "/rules", Icon: "scroll-text", Page: "rules", Order: 30},
				{Label: "Mail", URL: "/settings/mail", Page: "mail", Settings: true, Primary: true, Order: 15},
			}},
			{Module: pages.PauseModule},
			{Module: reviewpages.MailModule(reviewpages.Options{Clients: m.Clients, Tools: mc.Tools}), Recover: reviewpages.RecoverDrafts, Nav: []web.NavItem{
				{Section: "review", Label: "Loops", URL: "/loops", Icon: "list-checks", Page: "loops", Order: 20},
				{Section: "review", Label: "Packages", URL: "/watches", Icon: "package", Page: "watches", Order: 30},
			}},
			{Module: accountpages.Module, Templates: accountpages.Templates()},
		},
		Groups:   SettingGroups(),
		Commands: cmds,
		Leaves: map[string]module.Leaf{
			"live pause":      {Always: true},
			"live resume":     {Always: true},
			"live status":     {Always: true},
			"endpoints probe": {Always: true},
			"replay":          {Always: true},
		},
		Tools:        ToolDefinitions(),
		ProposeKinds: mailtools.Proposals(),
		Effects: func(d module.EffectDeps) module.Effects {
			e := &executors.Executors{DB: d.DB, Ledger: d.Ledger, Open: d.Open, Now: d.Now}
			r := &openloops.Reminders{DB: d.DB, Ledger: d.Ledger, Now: d.Now}
			effects := module.Effects{
				Proposals: slices.Concat(e.Proposals(module.Mail), r.Proposals(module.Mail)),
				Undo:      slices.Concat(e.Undo(), mailtools.Undo(d.Ledger, d.Open), live.Undo(d.Open, d.DB.Read), openloops.Undo()),
				Resolve:   live.Resolve(),
			}
			if d.Syncer != nil {
				effects.Sync = executors.SyncAfter(d.Syncer, d.Open)
			}
			return effects
		},
		Connections: slices.Concat(Connections(), tracking.Connections()),
		AccountSync: mailconn.AccountSync(),
	}
}
