package notifymodule

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/actionlink"
	actionlinkpages "github.com/jeiang/atrium/internal/actionlink/pages"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
	"github.com/jeiang/atrium/internal/notify/notifypages"
	"github.com/jeiang/atrium/internal/notify/notifytools"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Budget    notify.BudgetStatus
	Resources module.Resources
}

func SettingGroups() []settings.Group {
	return []settings.Group{
		{Name: "volume", Title: "Volume", Keys: []string{notify.KeyPerRunCap, notify.KeyNightlyCap}},
	}
}

func ToolDefinitions() []module.ToolDefinition {
	return []module.ToolDefinition{notifytools.Definition(func(tc *tool.Context) *notifyapp.Service { return notifyapp.New(tc.DB) })}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := notificationOperations()
	factory := func(d *web.Deps) *notifyapp.Service {
		_ = notify.EnsurePush(audit.With(d.Ctx, "notifications-bootstrap", audit.Job), d.DB)
		return notifyapp.New(d.DB).WithPresenter(actionlink.Present)
	}
	return module.Declaration{
		Operations: operations, Bindings: bindings,
		Name:     module.Notifications,
		Settings: slices.Concat(notify.Settings(), policy.Settings()),
		Pages: []web.Page{{Module: notifypages.Module(factory), Templates: notifypages.Templates(), Nav: []web.NavItem{
			{Section: "review", Label: "Inbox", URL: "/inbox", Icon: "inbox", Page: "inbox", Order: 50},
			{Label: "Notifications", URL: "/settings/notifications", Page: "notifications", Settings: true, Primary: true, Order: 40},
		}}, {Module: actionlinkpages.Module(factory), Templates: actionlinkpages.Templates()}},
		Groups:      SettingGroups(),
		Tools:       ToolDefinitions(),
		Connections: notify.Connections(),
		ConnectionNotice: func(ctx context.Context, d *db.DB, title, body string) error {
			if !audit.HasActor(ctx) {
				ctx = audit.With(ctx, "service:connection-health", audit.Job)
			}
			return notifyapp.ConnectionNotice()(ctx, d, title, body)
		},
		ProposalCreated: actionlink.ProposalCreated(),
		Services: []runtime.ServiceDefinition{{Name: "notifications-monitor", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return notifyapp.Monitor(ctx, d, mc.Env, m.Budget)
		}}},
	}
}
