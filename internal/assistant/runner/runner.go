package runner

import (
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
)

func New(tools registry.Source, store *ledger.Store, open mail.OpenFunc) (*loop.Runner, error) {
	reg, err := tools()
	if err != nil {
		return nil, err
	}
	r := &loop.Runner{Registry: reg, Ledger: store, MailOpen: open, Profile: kb.ProfileBlock}
	rs := research.New(r)
	if err := rs.Install(reg); err != nil {
		return nil, err
	}
	lk := lookup.New(r)
	if err := lk.Install(reg); err != nil {
		return nil, err
	}
	ws := websearch.New(r)
	if err := ws.Install(reg); err != nil {
		return nil, err
	}
	if err := reg.Add(report.Tool()); err != nil {
		return nil, err
	}
	return r, nil
}
