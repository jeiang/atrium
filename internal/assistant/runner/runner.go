package runner

import (
	"context"
	"errors"
	"sync"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/report"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents/lookup"
	"github.com/jeiang/atrium/internal/assistant/subagents/research"
	"github.com/jeiang/atrium/internal/assistant/subagents/websearch"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/tool"
)

func New(tools registry.Source, store *ledger.Store, open mail.OpenFunc) (*loop.Runner, error) {
	r := &loop.Runner{Ledger: store, MailOpen: open, Profile: kb.ProfileBlock}
	child := func(ctx context.Context, tc *tool.Context) *asstapp.Service {
		return asstapp.New(tc.DB, tc.Env, tc.Now).WithRuns(func() *service.Service {
			svc := service.New(ctx, tc.DB, tc.Cfg, tc.Env, r)
			svc.Client = tc.Client
			svc.Now = tc.Now
			return svc
		})
	}
	rs := research.New(child)
	lk := lookup.New(child)
	ws := websearch.New(child)
	var mu sync.Mutex
	var source, bound *registry.Registry
	r.Source = func() (*registry.Registry, error) {
		mu.Lock()
		defer mu.Unlock()
		reg, err := tools()
		if err != nil {
			return nil, err
		}
		if source == reg {
			return bound, nil
		}
		unbound := reg
		reg = reg.Clone()
		for _, handler := range []struct {
			name string
			fn   tool.Handler
		}{
			{research.Name, rs.Handle},
			{lookup.Name, lk.Handle},
			{websearch.Name, ws.Handle},
			{report.Name, report.Handle},
		} {
			name, fn := handler.name, handler.fn
			wrapped := func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				check := func() error { return reg.CheckOwner(ctx, name) }
				if err := check(); err != nil {
					return nil, err
				}
				return fn(ledger.WithAdmission(ctx, check), tc, args)
			}
			if err := reg.Handle(name, wrapped); err != nil {
				var unknown *registry.UnknownTool
				if errors.As(err, &unknown) {
					continue
				}
				return nil, err
			}
		}
		bound = reg
		source = unbound
		return reg, nil
	}
	reg, err := r.Source()
	if err != nil {
		return nil, err
	}
	r.Registry = reg
	return r, nil
}
