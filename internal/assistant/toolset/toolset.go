package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/tool"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	skillstools "github.com/jeiang/atrium/internal/assistant/tools/skills"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	contactstools "github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/notify"
)

func Undoers(env config.Env) *writes.Registry {
	r := writes.NewRegistry()
	profiletools.RegisterUndoer(r)
	calendartools.RegisterUndoers(r, env)
	mediatools.RegisterUndoer(r, env)
	notiontools.RegisterUndoers(r, env)
	mailtools.RegisterUndoer(r)
	scheduletools.RegisterUndoers(r)
	return r
}

// Registry loads every production tool package into one registry. The
// assistant module builds the live registry from the same packages; this
// entry point exists so the catalog generator can see the whole set.
func Registry() (*registry.Registry, error) {
	r := registry.New()
	for _, group := range [][]tool.Tool{
		mailtools.Tools(),
		calendartools.Tools(),
		mediatools.Tools(),
		notiontools.Tools(),
		profiletools.Tools(),
		scheduletools.Tools(),
		plantools.Tools(),
		recalltools.Tools(),
		skillstools.Tools(),
		proposetools.Tools(),
		grafanatools.Tools(),
		notify.Tools(),
		webfetch.Tools(),
		nightly.Tools(),
		[]tool.Tool{(&research.Research{}).Tool()},
		contactstools.Tools(),
	} {
		if err := r.Add(group...); err != nil {
			return nil, err
		}
	}
	return r, nil
}
