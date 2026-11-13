package contactsmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	contactsapp "github.com/jeiang/atrium/internal/contacts/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

func CardDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(module.Contacts, passrun.CardDAV, "Every 15 minutes: mirror the iCloud address books into the contacts directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := contactsapp.New(e.Service.DB, e.Service.Env).Sync(ctx, contactsapp.SyncInput{})
			return jobworker.Result{}, err
		}))
}
