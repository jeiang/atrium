package toolset

import (
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	filetools "github.com/jeiang/atrium/internal/assistant/tools/files"
	mailtools "github.com/jeiang/atrium/internal/assistant/tools/mail"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	opstools "github.com/jeiang/atrium/internal/assistant/tools/ops"
	scheduletools "github.com/jeiang/atrium/internal/assistant/tools/schedule"
	taskstools "github.com/jeiang/atrium/internal/assistant/tools/tasks"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/export"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/openloops"
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
		func(s *ledger.Store) error { return live.RegisterLedgerUndoers(s, open, d.Read) },
		func(s *ledger.Store) error {
			return calendar.RegisterLedgerUndoer(s, &calendar.Proposals{DB: d, Ledger: s, Now: now, Env: env})
		},
		openloops.RegisterLedgerUndoer,
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
