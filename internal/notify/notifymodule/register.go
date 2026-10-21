package notifymodule

import (
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
			module.Setting(notify.KeyURL),
			module.Setting(notify.KeyTopic),
		},
		Pages: []web.Page{{Module: notify.Module, Templates: notify.Templates()}},
		Tools: func(r *registry.Registry) error {
			return r.Add(notify.Tools()...)
		},
	}
}
