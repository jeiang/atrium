package contactsmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Module struct {
	Jobs jobworker.Dependencies
}

func (m Module) Declare(mc module.Context) module.Declaration {
	return module.Declaration{
		Name:     module.Contacts,
		Requires: Requirements(),
		Host:     contacts.HostSettings(),
		Jobs:     []scheduler.Definition{CardDAVJob(m.Jobs)},
		Commands: []*cli.Command{{Name: "contacts", Commands: []*cli.Command{syncCommand(mc.Env)}}},
		Tools:    ToolDefinitions(),
	}
}

func CardDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(module.Contacts, passrun.CardDAV, "Every 15 minutes: mirror the iCloud address books into the contacts directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := davmirror.Pass(ctx, e.Service.DB, passrun.CardDAV, mirrorContacts(e.Service.Env))
			return jobworker.Result{}, err
		}))
}
