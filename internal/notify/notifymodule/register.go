package notifymodule

import (
	"context"
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
	"github.com/jeiang/atrium/internal/notify/notifypages"
	"github.com/jeiang/atrium/internal/notify/notifytools"
	"github.com/jeiang/atrium/internal/proposals"
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
		return notifyapp.New(d.DB)
	}
	return module.Declaration{
		Operations: operations, Bindings: bindings,
		Name:     module.Notifications,
		Settings: notify.Settings(),
		Pages: []web.Page{{Module: notifypages.Module(factory), Templates: notifypages.Templates(), Nav: []web.NavItem{
			{Section: "review", Label: "Inbox", URL: "/inbox", Icon: "inbox", Page: "inbox", Order: 50},
			{Label: "Notifications", URL: "/settings/notifications", Page: "notifications", Settings: true, Primary: true, Order: 40},
		}}, {Module: func(_ chi.Router, d *web.Deps) { notificationRoutes(d, factory(d)) }}},
		Groups:      SettingGroups(),
		Tools:       ToolDefinitions(),
		Connections: notify.Connections(),
		ConnectionNotice: func(ctx context.Context, d *db.DB, title, body string) error {
			if !audit.HasActor(ctx) {
				ctx = audit.With(ctx, "service:connection-health", audit.Job)
			}
			return notifyapp.ConnectionNotice()(ctx, d, title, body)
		},
		ProposalCreated: proposalCreated,
		Services: []runtime.ServiceDefinition{{Name: "notifications-monitor", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return notifyapp.Monitor(ctx, d, mc.Env, m.Budget)
		}}},
	}
}

func proposalCreated(ctx context.Context, d *db.DB, p *proposals.Proposal) {
	if p.Kind != permission.Kind {
		return
	}
	_, _ = notify.Send(ctx, d, notify.Message{
		Channel: notify.Assistant, Title: "atrium: " + p.Kind + " proposal", Body: fmt.Sprintf("Proposal #%d (%s) is waiting for a decision.", p.ID, p.Kind),
		Click: fmt.Sprintf("/proposals/%d", p.ID), Occurrence: fmt.Sprintf("proposal:%d", p.ID),
	})
}
