package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	askoperatortools "github.com/jeiang/atrium/internal/assistant/tools/askoperator"
	attentiontool "github.com/jeiang/atrium/internal/assistant/tools/attention"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	filetools "github.com/jeiang/atrium/internal/assistant/tools/files"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	kbtools "github.com/jeiang/atrium/internal/assistant/tools/kb"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	memorytools "github.com/jeiang/atrium/internal/assistant/tools/memory"
	opstools "github.com/jeiang/atrium/internal/assistant/tools/ops"
	plantools "github.com/jeiang/atrium/internal/assistant/tools/plan"
	proposetools "github.com/jeiang/atrium/internal/assistant/tools/propose"
	recalltools "github.com/jeiang/atrium/internal/assistant/tools/recall"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	taskstools "github.com/jeiang/atrium/internal/assistant/tools/tasks"
	weathertools "github.com/jeiang/atrium/internal/assistant/tools/weather"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/config"
	contactstools "github.com/jeiang/atrium/internal/contacts/contactsmodule"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
)

func Undoers(env config.Env) *writes.Registry {
	r := writes.NewRegistry()
	kb.RegisterUndoer(r)
	calendartools.RegisterUndoers(r, env)
	mediatools.RegisterUndoer(r, env)
	mailtools.RegisterUndoer(r)
	scheduletools.RegisterUndoers(r)
	filetools.RegisterUndoer(r)
	taskstools.RegisterUndoer(r)
	opstools.RegisterUndoer(r)
	return r
}

func Ledger(d *db.DB, now func() float64) *ledger.Store {
	s := ledger.NewStore(d, now)
	if err := taskstools.RegisterLedgerUndoers(s); err != nil {
		panic(err)
	}
	return s
}

func Approvals() []tool.Tool {
	var out []tool.Tool
	for _, group := range [][]tool.Tool{mailtools.Tools(), calendartools.Tools(), mediatools.Tools()} {
		for _, t := range group {
			if t.Approval != nil {
				out = append(out, t)
			}
		}
	}
	return out
}

// Registry loads every production tool package into one registry. The
// assistant module builds the live registry from the same packages; this
// entry point exists so the catalog generator can see the whole set.
func Registry() (*registry.Registry, error) {
	r := registry.New(settings.New())
	for _, group := range [][]tool.Tool{
		askoperatortools.Tools(),
		attentiontool.Tools(),
		filetools.Tools(),
		memorytools.Tools(),
		mailtools.Tools(),
		calendartools.Tools(),
		mediatools.Tools(),
		kbtools.Tools(),
		opstools.Tools(),
		scheduletools.Tools(),
		plantools.Tools(),
		recalltools.Tools(),
		taskstools.Tools(),
		weathertools.Tools(),
		proposetools.Tools(),
		grafanatools.Tools(),
		notify.Tools(),
		webfetch.Tools(),
		[]tool.Tool{(&research.Research{}).Tool()},
		[]tool.Tool{lookup.New(nil).Tool()},
		[]tool.Tool{websearch.New(nil).Tool()},
		[]tool.Tool{report.Tool()},
		contactstools.Tools(),
	} {
		if err := r.Add(group...); err != nil {
			return nil, err
		}
	}
	return r, nil
}
