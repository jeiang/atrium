package contactsmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
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

func syncCommand(env config.Env) *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud address books into the contacts directory (read-only)", passrun.CardDAV,
		func(ctx context.Context, d *db.DB) (davmirror.Stats, error) {
			change, err := contactsapp.New(d, env).Sync(ctx, contactsapp.SyncInput{})
			return change.Data, err
		})
}
