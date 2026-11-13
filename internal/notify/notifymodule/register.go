package notifymodule

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/actionlink"
	actionlinkpages "github.com/jeiang/atrium/internal/actionlink/pages"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/settings"
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
	return []module.ToolDefinition{notify.Definition()}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	return module.Declaration{
		Name:     module.Notifications,
		Settings: slices.Concat(notify.Settings(), policy.Settings()),
		Pages: []web.Page{{Module: notify.Module(actionlinkpages.Load), Templates: notify.Templates(), Nav: []web.NavItem{
			{Section: "review", Label: "Inbox", URL: "/inbox", Icon: "inbox", Page: "inbox", Order: 50},
			{Label: "Notifications", URL: "/settings/notifications", Page: "notifications", Settings: true, Primary: true, Order: 40},
		}}},
		Groups:           SettingGroups(),
		Tools:            ToolDefinitions(),
		Connections:      notify.Connections(),
		ConnectionNotice: notify.ConnectionNotice(),
		ProposalCreated:  actionlink.ProposalCreated(),
		Services: []runtime.ServiceDefinition{{Name: "notifications-monitor", Run: func(ctx context.Context, _ *runtime.Services) error {
			d, _, err := m.Resources(ctx)
			if err != nil {
				return err
			}
			return notify.Monitor(ctx, d, mc.Env, m.Budget)
		}}},
	}
}
