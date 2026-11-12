package module

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

type Set struct {
	cat      *Catalog
	reg      *settings.Registry
	env      config.Env
	decls    []Declaration
	cur      atomic.Pointer[Snapshot]
	publish  sync.Mutex
	hooks    sync.Mutex
	starters []starter
	opener   Opener
}

type Opener func(context.Context) (*db.DB, config.Config, error)

type starter struct {
	owner string
	start func() error
}

func (s *Set) Catalog() *Catalog { return s.cat }

type setKey struct{}

func WithSet(ctx context.Context, s *Set) context.Context {
	return settings.WithCommit(context.WithValue(ctx, setKey{}, s), func(ctx context.Context, q db.Querier) (func(bool), error) {
		s.publish.Lock()
		snap, err := s.Evaluate(ctx, q)
		if err != nil {
			s.publish.Unlock()
			return nil, err
		}
		return func(committed bool) {
			if committed {
				s.store(snap)
			}
			s.publish.Unlock()
		}, nil
	})
}

func SetFrom(ctx context.Context) *Set {
	s, _ := ctx.Value(setKey{}).(*Set)
	return s
}

func IsEnabled(ctx context.Context, name string) bool {
	s := SetFrom(ctx)
	if s == nil {
		return false
	}
	snap := s.Current()
	return snap != nil && snap.Enabled(name)
}

func NewSet(cat *Catalog, reg *settings.Registry, env config.Env, open Opener) (*Set, error) {
	ordered, err := byNeeds(cat.Declarations())
	if err != nil {
		return nil, err
	}
	return &Set{cat: cat, reg: reg, env: env, decls: ordered, opener: open}, nil
}

func byNeeds(decls []Declaration) ([]Declaration, error) {
	placed := map[string]bool{}
	var out []Declaration
	remaining := slices.Clone(decls)
	for len(remaining) > 0 {
		i := slices.IndexFunc(remaining, func(d Declaration) bool {
			return !slices.ContainsFunc(d.Needs, func(dep string) bool { return !placed[dep] })
		})
		if i < 0 {
			names := make([]string, len(remaining))
			for j, d := range remaining {
				names[j] = d.Name
			}
			return nil, &DependencyCycle{Modules: names}
		}
		placed[remaining[i].Name] = true
		out = append(out, remaining[i])
		remaining = slices.Delete(remaining, i, i+1)
	}
	return out, nil
}

type Snapshot struct {
	set         *Set
	statuses    []Status
	generations map[string]uint64
	once        sync.Once
	registry    *registry.Registry
	regErr      error
}

func (s *Set) Current() *Snapshot { return s.cur.Load() }

func (s *Set) Refresh(ctx context.Context, q db.Querier) error {
	s.publish.Lock()
	defer s.publish.Unlock()
	snap, err := s.Evaluate(ctx, q)
	if err != nil {
		return err
	}
	s.store(snap)
	return nil
}

func (s *Set) store(snap *Snapshot) {
	prev := s.cur.Swap(snap)
	for _, st := range snap.statuses {
		var was Status
		if prev != nil {
			was, _ = prev.Status(st.Name)
		}
		if st.State == Unavailable && (was.State != Unavailable || was.Err != st.Err) {
			log.Error("module is unavailable", logx.Module, st.Name, logx.State, string(st.State), logx.Reason, st.Err)
		}
	}
	s.hooks.Lock()
	var ready []starter
	s.starters = slices.DeleteFunc(s.starters, func(st starter) bool {
		if !snap.Enabled(st.owner) {
			return false
		}
		ready = append(ready, st)
		return true
	})
	s.hooks.Unlock()
	for _, st := range ready {
		if err := st.start(); err != nil {
			log.Error("module start failed", logx.Module, st.owner, logx.Err, err.Error())
		}
	}
}

func (s *Set) OnEnable(owner string, start func() error) error {
	s.hooks.Lock()
	if !s.enabled(owner) {
		s.starters = append(s.starters, starter{owner: owner, start: start})
		s.hooks.Unlock()
		return nil
	}
	s.hooks.Unlock()
	return start()
}

func (s *Set) Evaluate(ctx context.Context, q db.Querier) (*Snapshot, error) {
	probe := Probe{Settings: s.reg, Q: q, Env: s.env}
	statuses, _, err := evaluate(ctx, s.decls, func(ctx context.Context, r Declaration, states map[string]State) ([]Unmet, bool, error) {
		unmet, err := s.unmet(ctx, probe, r, states)
		if err != nil {
			return nil, false, err
		}
		found, err := s.reg.Resolve(ctx, q, FlagKey(r.Name))
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", FlagKey(r.Name), err)
		}
		return unmet, found.Value == "true", nil
	})
	if err != nil {
		return nil, err
	}
	generations := make(map[string]uint64, len(statuses))
	current := s.Current()
	for _, status := range statuses {
		generation := uint64(1)
		if current != nil {
			generation = current.Generation(status.Name)
			if current.Enabled(status.Name) != (status.State == Enabled) {
				generation++
			}
		}
		generations[status.Name] = generation
	}
	return &Snapshot{set: s, statuses: statuses, generations: generations}, nil
}

type check func(ctx context.Context, r Declaration, states map[string]State) ([]Unmet, bool, error)

func evaluate(ctx context.Context, decls []Declaration, fn check) ([]Status, map[string]State, error) {
	states := map[string]State{}
	statuses := make([]Status, 0, len(decls))
	for _, r := range decls {
		st := Status{Name: r.Name, State: Enabled}
		if !r.Core {
			unmet, on, err := fn(ctx, r, states)
			var broken *requirementError
			switch {
			case errors.As(err, &broken):
				st.State, st.Err = Unavailable, broken.Error()
			case err != nil:
				return nil, nil, &DeclarationError{Module: Core, Field: "evaluation", Err: err}
			case len(unmet) > 0:
				st.State, st.Unmet = NeedsSettings, unmet
			case !on:
				st.State = Off
			}
		}
		states[r.Name] = st.State
		statuses = append(statuses, st)
	}
	return statuses, states, nil
}

func (s Status) Reason() string {
	if s.Err != "" {
		return s.Err
	}
	return joinReasons(s.Unmet)
}

func (n *Snapshot) Status(name string) (Status, bool) {
	for _, st := range n.statuses {
		if st.Name == name {
			return st, true
		}
	}
	return Status{}, false
}

func (n *Snapshot) Attention() []attention.Source {
	var out []attention.Source
	for _, r := range n.set.decls {
		if r.Core || n.Enabled(r.Name) {
			out = append(out, r.Attention...)
		}
	}
	return out
}

func (n *Snapshot) Enabled(name string) bool {
	st, ok := n.Status(name)
	return ok && st.State == Enabled
}

func (n *Snapshot) Generation(owner string) uint64 { return n.generations[owner] }

func (n *Snapshot) Registry() (*registry.Registry, error) {
	n.once.Do(func() { n.registry, n.regErr = n.buildRegistry() })
	return n.registry, n.regErr
}

func (n *Snapshot) buildRegistry() (*registry.Registry, error) {
	out := registry.New(n.set.reg)
	for _, d := range n.set.decls {
		if !n.Enabled(d.Name) {
			continue
		}
		for _, def := range d.Tools {
			owner, generation := d.Name, n.Generation(d.Name)
			out.SetOwner(def.Tool.Name, func(ctx context.Context) error { return Require(ctx, owner, generation) })
			wrap := func(t tool.Tool) tool.Tool {
				if t.Handler != nil {
					handler := t.Handler
					t.Handler = func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
						if err := Require(ctx, owner, generation); err != nil {
							return nil, err
						}
						ctx = ledger.WithAdmission(ctx, func() error {
							return Require(ctx, owner, generation)
						})
						return handler(ctx, tc, args)
					}
				}
				return t
			}
			def.Tool = wrap(def.Tool)
			var err error
			if def.Tool.Handler == nil && !def.Tool.Server {
				err = out.Declare(def.Tool)
			} else {
				err = out.Add(def.Tool)
			}
			if err != nil {
				return nil, err
			}
			if def.Bind != nil {
				bind := def.Bind
				out.Bind(def.Tool.Name, func(ctx context.Context, q db.Querier, t tool.Tool) (tool.Tool, bool, error) {
					bound, ok, err := bind(ctx, q, t)
					return wrap(bound), ok, err
				})
			}
		}
	}
	if _, err := out.All(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Set) Guard(ctx context.Context, q db.Querier, key, value string) error {
	for _, r := range s.decls {
		if key != FlagKey(r.Name) || value != "true" {
			continue
		}
		snap, err := s.Evaluate(ctx, q)
		if err != nil {
			return err
		}
		st, _ := snap.Status(r.Name)
		if len(st.Unmet) > 0 {
			return &NeedsSettingsError{Module: r.Name, Unmet: st.Unmet}
		}
		if st.State == Unavailable {
			return &UnavailableError{Module: r.Name, Err: st.Err}
		}
	}
	return nil
}
