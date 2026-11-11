package module

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("atrium.module")

func (s *Set) enabled(name string) bool {
	snap := s.Current()
	return snap != nil && snap.Enabled(name)
}

func (s *Set) Pages() []web.Page {
	pages := []web.Page{{Globals: map[string]any{"module_enabled": s.enabled}}}
	for _, r := range s.regs {
		for _, p := range r.Pages {
			if p.Module == nil && p.Start == nil && len(p.Nav) == 0 {
				continue
			}
			if !r.Core {
				p = s.gatePage(r.Name, p)
			}
			pages = append(pages, p)
		}
	}
	return append(pages, settingspage.Page(s.settingPages()))
}

func (s *Set) settingState(ctx context.Context, q db.Querier, name string) (settings.ModuleView, error) {
	held, err := s.Held(ctx, q, name)
	if err != nil {
		return settings.ModuleView{}, err
	}
	view := settings.ModuleView{On: held.Result == "", Running: s.enabled(name)}
	if held.Result == HoldNeeds {
		view.Reason = held.Detail
	}
	source, pinned, err := s.reg.Pinned(FlagKey(name))
	if pinned {
		view.Pinned = source
	}
	return view, err
}

func (s *Set) settingMark(name string) string {
	snap := s.Current()
	if snap == nil {
		return ""
	}
	switch st, _ := snap.Status(name); st.State {
	case NeedsSettings:
		return "warning"
	case Off:
		return "off"
	}
	return "on"
}

func (s *Set) settingPages() ([]settingspage.Group, []settingspage.Generic, settingspage.Control) {
	var groups []settingspage.Group
	var generic []settingspage.Generic
	control := settingspage.Control{State: s.settingState, Mark: s.settingMark}
	for _, r := range s.regs {
		if !r.Core {
			for _, p := range r.Pages {
				for _, item := range p.Nav {
					if item.Settings && item.Primary {
						control.Modules = append(control.Modules, settingspage.Module{Name: r.Name, URL: item.URL})
					}
				}
			}
		}
		home, tabs := "", map[string]string{}
		for _, p := range r.Pages {
			for _, item := range p.Nav {
				if !item.Settings {
					continue
				}
				tabs[item.URL] = item.Page
				if home == "" {
					home = item.URL
				}
			}
		}
		own := home == "" && !r.Core && settingspage.Wanted(r.Name)
		for _, g := range r.Settings {
			g.Module = r.Name
			at := settingspage.Group{Group: g, URL: g.Page}
			if g.Page == "" {
				at.URL, own = home, true
				if home == "" {
					at.URL = "/settings/" + r.Name
					tabs[at.URL] = "module:" + r.Name
				}
			}
			at.Tab = tabs[at.URL]
			groups = append(groups, at)
		}
		if own && home == "" {
			generic = append(generic, settingspage.Generic{Module: r.Name, URL: "/settings/" + r.Name, Tab: "module:" + r.Name})
			if !r.Core {
				control.Modules = append(control.Modules, settingspage.Module{Name: r.Name, URL: "/settings/" + r.Name})
			}
		}
	}
	return groups, generic, control
}

func (s *Set) gatePage(owner string, p web.Page) web.Page {
	for i := range p.Nav {
		if p.Nav[i].Module == "" {
			p.Nav[i].Module = owner
		}
	}
	inner := p.Module
	p.Module = func(r chi.Router, d *web.Deps) {
		if s.Current().Enabled(owner) {
			inner(r, d)
		}
	}
	if public := p.Public; public != nil {
		p.Public = func(cfg config.Config, env config.Env, d *db.DB, reg *settings.Registry) func(*http.Request) bool {
			check := public(cfg, env, d, reg)
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
				sdnotify.New(func(name string) string { v, _ := env.Env(name); return v }).Ready()
				return cli.ExitOK
			}
			for _, name := range needs {
				if st, _ := snap.Status(name); st.State != Enabled {
					skip(path, st, "needed module is not enabled, command skipped")
					sdnotify.New(func(name string) string { v, _ := env.Env(name); return v }).Ready()
					return cli.ExitOK
				}
			}
			return handler(env)
		}
	}
}

func skip(path string, st Status, msg string) {
	log.Info(msg, logx.Module, st.Name, logx.State, string(st.State), logx.Reason, st.Reason(), logx.Command, strings.TrimSpace(path))
}

// Open loads the configuration, opens the database and boots it. It is the
// serve command's only path to the application database.
func (s *Set) Open(ctx context.Context) (*db.DB, config.Config, error) {
	cfg, err := config.Load(s.env)
	if err != nil {
		return nil, config.Config{}, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, config.Config{}, err
	}
	if err := config.Boot(ctx, s.env, d, cfg); err != nil {
		_ = d.Close()
		return nil, config.Config{}, err
	}
	store, err := connections.New(d, config.From(ctx).Host(), connections.DefaultRegistry())
	if err != nil {
		_ = d.Close()
		return nil, config.Config{}, err
	}
	if err := store.ReconcileHost(ctx); err != nil {
		_ = d.Close()
		return nil, config.Config{}, err
	}
	connections.SetDefault(d.Read, store)
	return d, cfg, nil
}

func (s *Set) open(ctx context.Context) (*db.DB, error) {
	d, _, err := s.Open(ctx)
	return d, err
}

// Start seeds the module flags and evaluates the one startup snapshot. A
// failure to evaluate leaves the process serving core only, matching the
// behaviour the module gate has always had.
func (s *Set) Start(ctx context.Context, d *web.Deps) error {
	if err := s.Seed(ctx, d.DB); err != nil {
		log.Error("module evaluation failed, serving core only", logx.Err, err.Error())
		s.cur.Store(s.coreOnly())
		return nil
	}
	snap, err := s.Evaluate(ctx, d.DB.Read)
	if err != nil {
		log.Error("module evaluation failed, serving core only", logx.Err, err.Error())
		snap = s.coreOnly()
	}
	s.cur.Store(snap)
	return nil
}

func (s *Set) load(ctx context.Context) (*Snapshot, error) {
	d, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
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
