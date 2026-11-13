package mailmodule

import (
	"context"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/service"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Clients   func(config.Config) mail.OpenFunc
	Now       func() float64
	Jobs      jobworker.Dependencies
	Resources module.Resources
}

func (m Module) classifiers(ctx context.Context, env config.Env, d *db.DB, cfg config.Config, now func() float64) (*classifier.Service, error) {
	if m.Jobs.Classifier == nil {
		return nil, errors.New("the mail pass is not bound to a classifier")
	}
	svc := service.New(ctx, d, cfg, env, nil)
	svc.Now = now
	return m.Jobs.Classifier(ctx, svc)
}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Drafts(), attentionsources.Loops(), attentionsources.Commitments(), attentionsources.Packages()}
}

func Requirements() []module.Requirement {
	return []module.Requirement{
		{Name: "service/embedding", Check: func(ctx context.Context, p module.Probe) (string, error) {
			return endpoint.EmbeddingConfigured(ctx, p.Q)
		}},
		{Name: "service/embedding test", Check: func(ctx context.Context, p module.Probe) (string, error) {
			return mail.EmbeddingTestFailure(ctx, p.Q)
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
	return mailtools.Definitions(mailToolService)
}

func ProposeKinds() []proposals.Definition {
	return mailtools.Proposals(mailToolService)
}

func (m Module) Declare(mc module.Context) module.Declaration {
	mailCmds := mailcmd.Options{Env: mc.Env, Clients: m.Clients, Now: m.Now, Classifiers: m.classifiers, Application: mailCLIService(mc.Env)}
	cmds := []*cli.Command{
		mailCmds.SyncCommand(),
		mailCmds.RulesCommand(),
		mailCmds.ShadowReportCommand(),
		mailCmds.ReindexCommand(),
		mailCmds.LiveCommand(),
		mailCmds.ReplayCommand(),
	}
	cmds = append(cmds, mailCmds.ScanCommands()...)
	cmds = append(cmds, mailCmds.RefreshCommands()...)
	cmds = append(cmds, mailCmds.TrackingCommands()...)
	operations, bindings := mailReadOperations()
	accountOps, accountBindings := mailAccountOperations()
	operations = append(operations, accountOps...)
	bindings = append(bindings, accountBindings...)
	ruleOps, ruleBindings := mailRuleOperations()
	operations = append(operations, ruleOps...)
	bindings = append(bindings, ruleBindings...)
	draftOps, draftBindings := mailDraftOperations()
	operations = append(operations, draftOps...)
	bindings = append(bindings, draftBindings...)
	mailboxOps, mailboxBindings := mailMailboxOperations()
	operations = append(operations, mailboxOps...)
	bindings = append(bindings, mailboxBindings...)
	loopOps, loopBindings := mailLoopOperations()
	operations = append(operations, loopOps...)
	bindings = append(bindings, loopBindings...)
	scanOps, scanBindings := mailScanOperations()
	operations = append(operations, scanOps...)
	bindings = append(bindings, scanBindings...)
	passOps, passBindings := mailPassOperations()
	operations = append(operations, passOps...)
	bindings = append(bindings, passBindings...)
	return module.Declaration{
		Name:           module.Mail,
		Operations:     operations,
		Bindings:       append(bindings, finalBindings()...),
		Recover:        reviewpages.RecoverDrafts,
		Settings:       slices.Concat(mail.Settings(), tracking.SettingsDefs()),
		Host:           mail.HostSettings(),
		Attention:      AttentionSources(),
		Jobs:           []scheduler.Definition{mailcmd.TrackingJob(m.Jobs), m.syncJob(mc.Env), m.scanJob(mc.Env)},
		ServiceSets:    m.watchServices(mc.Env),
		ServiceSetName: "mail/watch",
		Requires:       Requirements(),
		Pages: []web.Page{
			{Module: pages.Module(mailPageService), Templates: pages.Templates(), Globals: pages.Globals(), Nav: []web.NavItem{
				{Section: "review", Label: "Mail decisions", URL: "/triage", Icon: "route", Page: "triage", Order: 40},
				{Section: "library", Label: "Mail rules", URL: "/rules", Icon: "scroll-text", Page: "rules", Order: 30},
				{Label: "Mail", URL: "/settings/mail", Page: "mail", Settings: true, Primary: true, Order: 15},
			}},
			{Module: pages.PauseModule(mailPageService)},
			{Module: reviewpages.MailModule(reviewpages.Options{Clients: m.Clients, Tools: mc.Tools, Application: mailPageService}), Nav: []web.NavItem{
				{Section: "review", Label: "Loops", URL: "/loops", Icon: "list-checks", Page: "loops", Order: 20},
				{Section: "review", Label: "Packages", URL: "/watches", Icon: "package", Page: "watches", Order: 30},
			}},
			{Module: accountpages.Module(mailPageService, mailSettingsService), Templates: accountpages.Templates()},
		},
		Groups:   SettingGroups(),
		Commands: cmds,
		Leaves: map[string]module.Leaf{
			"live pause":  {Always: true},
			"live resume": {Always: true},
			"live status": {Always: true},
			"replay":      {Always: true},
		},
		Tools:        ToolDefinitions(),
		ProposeKinds: ProposeKinds(),
		Effects: func(d module.EffectDeps) module.Effects {
			e := &executors.Executors{DB: d.DB, Ledger: d.Ledger, Open: d.Open, Now: d.Now}
			r := &openloops.Reminders{DB: d.DB, Ledger: d.Ledger, Now: d.Now}
			effects := module.Effects{
				Proposals: slices.Concat(e.Proposals(module.Mail), r.Proposals(module.Mail)),
				Undo:      slices.Concat(e.Undo(), mailtools.Undo(d.Ledger, d.Open), live.Undo(d.Open, d.DB), openloops.Undo()),
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
