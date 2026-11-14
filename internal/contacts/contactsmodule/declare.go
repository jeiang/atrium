package contactsmodule

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts"
	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
	"github.com/jeiang/atrium/internal/db"
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
		Commands:   []*cli.Command{{Name: "contacts", Commands: []*cli.Command{syncCommand(openSync(mc.Env))}}},
		Tools:      ToolDefinitions(),
		Operations: operations,
		Bindings:   bindings,
		Pages:      []web.Page{{Module: func(_ chi.Router, d *web.Deps) { contactRoutes(d) }}},
	}
}

func openSync(env config.Env) syncOpen {
	return func(ctx context.Context) (*contactsapp.Service, contactsapp.Config, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return nil, cfg, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, cfg, nil, err
		}
		if err := config.Boot(ctx, env, d, cfg); err != nil {
			_ = d.Close()
			return nil, cfg, nil, err
		}
		return contactsapp.New(d, env), cfg, func() { _ = d.Close() }, nil
	}
}
