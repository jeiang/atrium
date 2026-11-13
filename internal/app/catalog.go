package app

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/assistantmodule"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/tools/grafana/grafanamodule"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/calendar/calendarmodule"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailmodule"
	"github.com/jeiang/atrium/internal/media/mediamodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/notify/notifymodule"
	"github.com/jeiang/atrium/internal/notion/notionmodule"
)

type Dependencies struct {
	Version  string
	Guard    func(context.Context, db.Querier, string, string) error
	Clients  func(config.Config) mail.OpenFunc
	Now      func() float64
	Env      config.Env
	Budget   notify.BudgetStatus
	Bindings *Bindings
}

func Builtins(d Dependencies) []module.Builder {
	jobs := jobworker.Dependencies{Service: d.Bindings.Assistant, Classifier: classifierFor, Now: d.Now, Attention: attentionSources}
	return []module.Builder{
		coremodule.Module{Version: d.Version, Guard: d.Guard, Clients: d.Clients, Now: d.Now, Resources: d.Bindings.Resources, Jobs: jobs},
		contactsmodule.Module{},
		mailmodule.Module{Clients: d.Clients, Now: d.Now, Jobs: jobs},
		calendarmodule.Module{Jobs: jobs},
		notionmodule.Module{Env: d.Env},
		mediamodule.Module{Now: d.Now, Jobs: jobs},
		assistantmodule.Module{Now: d.Now, ProposeKinds: mailtools.Proposals(), Open: d.Bindings.OpenAssistant, Jobs: jobs, Start: d.Bindings.StartAssistant},
		notifymodule.Module{Budget: d.Budget, Resources: d.Bindings.Resources},
		grafanamodule.Module{Jobs: jobs},
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

func attentionSources(ctx context.Context) []attention.Source {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return nil
	}
	return set.Current().Attention()
}
