package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/assistantmodule"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	grafanamodule "github.com/jeiang/atrium/internal/assistant/tools/grafana/grafanamodule"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/calendar/calendarmodule"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/mail/mailmodule"
	"github.com/jeiang/atrium/internal/media/mediamodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify/notifymodule"
	"github.com/jeiang/atrium/internal/notion/notionmodule"
	"github.com/jeiang/atrium/sdk"
)

func Undoers(env config.Env) *writes.Registry {
	r := writes.NewRegistry()
	profiletools.RegisterUndoer(r)
	calendartools.RegisterUndoers(r)
	mailtools.RegisterUndoers(r)
	scheduletools.RegisterUndoers(r)
	mediatools.RegisterUndoer(r, env)
	notiontools.RegisterUndoers(r, env)
	return r
}

func Registry() (*registry.Registry, error) {
	mods := append([]module.Module{
		contactsmodule.Module{},
		mailmodule.Module{},
		calendarmodule.Module{},
		notionmodule.Module{},
		mediamodule.Module{},
		assistantmodule.Module{},
		notifymodule.Module{},
		grafanamodule.Module{},
	}, sdk.Modules()...)
	out := registry.New()
	for _, m := range mods {
		if add := m.Register(module.Context{}).Tools; add != nil {
			if err := add(out); err != nil {
				return nil, err
			}
		}
	}
	if err := research.New(nil).Install(out); err != nil {
		return nil, err
	}
	return out, nil
}
