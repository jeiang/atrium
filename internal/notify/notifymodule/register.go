package notifymodule

import (
	actionlinkpages "github.com/jeiang/atrium/internal/actionlink/pages"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct{}

func (Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name: module.Notifications,
		Requires: []module.Requirement{
			module.Setting(notify.KeyPushSubject),
		},
		Pages: []web.Page{{Module: notify.Module(actionlinkpages.Load), Templates: notify.Templates(), Nav: []web.NavItem{
			{Section: "review", Label: "Inbox", URL: "/inbox", Icon: "inbox", Page: "inbox", Order: 50},
			{Label: "Notifications", URL: "/settings/notifications", Page: "notifications", Settings: true, Primary: true, Order: 40},
		}}},
		Tools: func(r *registry.Registry) error {
			return r.Add(notify.Tools()...)
		},
	}
}
