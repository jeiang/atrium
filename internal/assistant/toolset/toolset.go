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
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

func Ledger(d *db.DB, env config.Env, open mail.OpenFunc, now func() float64) *ledger.Store {
	s := ledger.NewStore(d, now)
	registers := []func(*ledger.Store) error{
		taskstools.RegisterLedgerUndoers,
		kb.RegisterLedgerUndoers,
		scheduletools.RegisterLedgerUndoers,
		filetools.RegisterLedgerUndoer,
		opstools.RegisterLedgerUndoer,
		func(s *ledger.Store) error { return calendartools.RegisterLedgerUndoers(s, env) },
		func(s *ledger.Store) error { return mediatools.RegisterLedgerUndoer(s, env) },
		func(s *ledger.Store) error { return mailtools.RegisterLedgerUndoer(s, open) },
		func(s *ledger.Store) error { return export.RegisterLedgerResolves(s) },
		export.RegisterLedgerObsidian,
	}
	for _, register := range registers {
		if err := register(s); err != nil {
			panic(err)
		}
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
