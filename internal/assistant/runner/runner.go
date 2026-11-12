package runner

import (
	"errors"

	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/tool"
)

func New(tools registry.Source, store *ledger.Store, open mail.OpenFunc) (*loop.Runner, error) {
	reg, err := tools()
	if err != nil {
		return nil, err
	}
	r := &loop.Runner{Registry: reg, Ledger: store, MailOpen: open, Profile: kb.ProfileBlock}
	rs := research.New(r)
	lk := lookup.New(r)
	ws := websearch.New(r)
	for _, handler := range []struct {
		name string
		fn   tool.Handler
	}{
		{research.Name, rs.Handle},
		{lookup.Name, lk.Handle},
		{websearch.Name, ws.Handle},
		{report.Name, report.Handle},
	} {
		if err := reg.Handle(handler.name, handler.fn); err != nil {
			var unknown *registry.UnknownTool
			if errors.As(err, &unknown) {
				continue
			}
			return nil, err
		}
	}
	return r, nil
}
