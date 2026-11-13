package contactsmodule

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/module"
)

type Module struct{}

func Requirements() []module.Requirement {
	return []module.Requirement{module.HostSetting(contacts.DirKey)}
}

func ToolDefinitions() []module.ToolDefinition {
	return []module.ToolDefinition{Definition()}
}

func (Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := contactsOperations()
	return module.Declaration{
		Name:       module.Contacts,
		Requires:   Requirements(),
		Host:       contacts.HostSettings(),
		Commands:   []*cli.Command{{Name: "contacts", Commands: []*cli.Command{syncCommand(mc.Env)}}},
		Tools:      ToolDefinitions(),
		Operations: operations,
		Bindings:   bindings,
	}
}
