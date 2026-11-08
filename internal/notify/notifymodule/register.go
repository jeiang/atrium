package notifymodule

import (
	"context"

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
			anyOf(module.Setting(notify.KeyURL), module.Setting(notify.KeyPushSubject)),
		},
		Pages: []web.Page{{Module: notify.Module, Templates: notify.Templates(), Nav: []web.NavItem{
			{Section: "review", Label: "Inbox", URL: "/inbox", Icon: "inbox", Page: "inbox", Order: 50},
			{Label: "Notifications", URL: "/settings/notifications", Page: "notifications", Settings: true, Primary: true, Order: 60},
		}}},
		Tools: func(r *registry.Registry) error {
			return r.Add(notify.Tools()...)
		},
	}
}

func anyOf(a, b module.Requirement) module.Requirement {
	return module.Requirement{Name: a.Name + " or " + b.Name, Check: func(ctx context.Context, p module.Probe) (string, error) {
		first, err := a.Check(ctx, p)
		if err != nil || first == "" {
			return "", err
		}
		second, err := b.Check(ctx, p)
		if err != nil || second == "" {
			return "", err
		}
		return first + "; " + second, nil
	}}
}
