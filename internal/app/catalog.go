package app

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/assistantmodule"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/tools/grafana/grafanamodule"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/calendar/calendarmodule"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/db"
	kbcallables "github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/mailmodule"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/media/mediamodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notify/notifymodule"
	"github.com/jeiang/atrium/internal/notion/notionmodule"
	"github.com/jeiang/atrium/internal/serverops"
)

type Dependencies struct {
	Version string
	Guard   func(context.Context, db.Querier, string, string) error
	Clients func(config.Config) mail.OpenFunc
	Now     func() float64
	Env     config.Env
	Budget  notify.BudgetStatus
}

func Builtins(d Dependencies) []module.Builder {
	return []module.Builder{
		coremodule.Module{Version: d.Version, Guard: d.Guard, Clients: d.Clients, Now: d.Now},
		contactsmodule.Module{},
		mailmodule.Module{Clients: d.Clients, Now: d.Now},
		calendarmodule.Module{},
		notionmodule.Module{Env: d.Env},
		mediamodule.Module{Now: d.Now},
		assistantmodule.Module{Now: d.Now, Callables: []map[string]worker.Callable{calendarcmd.Callables(), mediacmd.Callables(), mailcmd.Callables(), kbcallables.Callables(), serverops.Callables()}},
		notifymodule.Module{Budget: d.Budget},
		grafanamodule.Module{},
	}
}

func Catalog(d Dependencies, mc module.Context) (*module.Catalog, error) {
	builders := Builtins(d)
	decls := make([]module.Declaration, 0, len(builders))
	for _, b := range builders {
		decls = append(decls, b.Declare(mc))
	}
	return module.Compile(decls)
}

func Docs() (*module.Catalog, error) {
	return Catalog(Dependencies{}, module.Context{})
}
