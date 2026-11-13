package contactsmodule

import (
	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Jobs jobworker.Dependencies
}

func Requirements() []module.Requirement {
	return []module.Requirement{module.HostSetting(contacts.DirKey)}
}

func ToolDefinitions() []module.ToolDefinition {
	return []module.ToolDefinition{Definition()}
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := contactsOperations()
	return module.Declaration{
		Name:       module.Contacts,
		Requires:   Requirements(),
		Host:       contacts.HostSettings(),
		Jobs:       []scheduler.Definition{CardDAVJob(m.Jobs)},
		Commands:   []*cli.Command{{Name: "contacts", Commands: []*cli.Command{syncCommand(mc.Env)}}},
		Tools:      ToolDefinitions(),
		Operations: operations,
		Bindings:   bindings,
		Pages:      []web.Page{{Module: func(_ chi.Router, d *web.Deps) { contactRoutes(d) }}},
	}
}
