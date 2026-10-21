package module

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("atrium.module")

func (s *Set) Pages() []web.Page {
	pages := []web.Page{{Module: func(_ chi.Router, d *web.Deps) {
		err := s.Seed(context.Background(), d.DB)
		var snap *Snapshot
		if err == nil {
			snap, err = s.Evaluate(context.Background(), d.DB.Read)
		}
		if err != nil {
			log.Error("module evaluation failed, serving core only", logx.Err, err.Error())
			snap = s.coreOnly()
		}
		s.cur.Store(snap)
	}}}
	for _, r := range s.regs {
		for _, p := range r.Pages {
			if p.Module == nil {
				continue
			}
			if !r.Core {
				p = s.gatePage(r.Name, p)
			}
			pages = append(pages, p)
		}
	}
	return pages
}

func (s *Set) gatePage(owner string, p web.Page) web.Page {
	inner := p.Module
	p.Module = func(r chi.Router, d *web.Deps) {
		if s.Current().Enabled(owner) {
			inner(r, d)
		}
	}
	if public := p.Public; public != nil {
		p.Public = func(cfg config.Config, env config.Env) func(*http.Request) bool {
			check := public(cfg, env)
			return func(r *http.Request) bool {
				snap := s.Current()
				return snap != nil && snap.Enabled(owner) && check(r)
			}
		}
	}
	return p
}

func (s *Set) Commands() []*cli.Command {
	var out []*cli.Command
	for _, r := range s.regs {
		for _, c := range r.Commands {
			out = append(out, s.gateCommand(r, c, c.Name, Leaf{}))
		}
	}
	return out
}

func (s *Set) gateCommand(r Registration, c *cli.Command, path string, inherited Leaf) *cli.Command {
	rule, ok := r.Leaves[path]
	if !ok {
		rule = inherited
	}
	out := *c
	if out.Setup != nil && !rule.Always && (!r.Core || len(rule.Needs) > 0) {
		out.Setup = s.gateSetup(r.Name, path, rule.Needs, c.Setup)
	}
	out.Commands = nil
	for _, sub := range c.Commands {
		out.Commands = append(out.Commands, s.gateCommand(r, sub, path+" "+sub.Name, rule))
	}
	return &out
}

func (s *Set) gateSetup(owner, path string, needs []string, setup func(*cli.Args) cli.Handler) func(*cli.Args) cli.Handler {
	return func(a *cli.Args) cli.Handler {
		handler := setup(a)
		if handler == nil {
			return nil
		}
		return func(env *cli.Env) int {
			snap, err := s.load(env.Ctx)
			if err != nil {
				return cli.Fail(env, err)
			}
			if st, _ := snap.Status(owner); st.State != Enabled {
				skip(path, st, "module is not enabled, command skipped")
				return cli.ExitOK
			}
			for _, name := range needs {
				if st, _ := snap.Status(name); st.State != Enabled {
					skip(path, st, "needed module is not enabled, command skipped")
					return cli.ExitOK
				}
			}
			return handler(env)
		}
	}
}

func skip(path string, st Status, msg string) {
	log.Info(msg, logx.Module, st.Name, logx.State, string(st.State), logx.Reason, st.Reason, logx.Command, strings.TrimSpace(path))
}

func (s *Set) load(ctx context.Context) (*Snapshot, error) {
	cfg, err := config.Load(s.env)
	if err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return nil, err
	}
	if err := s.Seed(ctx, d); err != nil {
		return nil, err
	}
	snap, err := s.Evaluate(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	s.cur.Store(snap)
	return snap, nil
}
