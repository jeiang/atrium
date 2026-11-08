package module

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Set struct {
	reg  *settings.Registry
	env  config.Env
	regs []Registration
	cur  atomic.Pointer[Snapshot]
}

var active atomic.Pointer[Set]

func IsEnabled(name string) bool {
	s := active.Load()
	if s == nil {
		return false
	}
	snap := s.Current()
	return snap != nil && snap.Enabled(name)
}

func NewSet(reg *settings.Registry, env config.Env, mods ...Module) (*Set, error) {
	s := &Set{reg: reg, env: env}
	var regs []Registration
	for _, m := range mods {
		regs = append(regs, m.Register(Context{Current: s.Current, Evaluate: s.Evaluate, Env: env}))
	}
	s.regs = regs
	if err := s.validate(); err != nil {
		return nil, err
	}
	ordered, err := byNeeds(regs)
	if err != nil {
		return nil, err
	}
	s.regs = ordered
	active.Store(s)
	return s, nil
}

func byNeeds(regs []Registration) ([]Registration, error) {
	placed := map[string]bool{}
	var out []Registration
	remaining := slices.Clone(regs)
	for len(remaining) > 0 {
		i := slices.IndexFunc(remaining, func(r Registration) bool {
			return !slices.ContainsFunc(r.Needs, func(dep string) bool { return !placed[dep] })
		})
		if i < 0 {
			names := make([]string, len(remaining))
			for j, r := range remaining {
				names[j] = r.Name
			}
			return nil, &DependencyCycle{Modules: names}
		}
		placed[remaining[i].Name] = true
		out = append(out, remaining[i])
		remaining = slices.Delete(remaining, i, i+1)
	}
	return out, nil
}

func (s *Set) validate() error {
	seen := map[string]bool{}
	for _, r := range s.regs {
		if r.Name == "" || seen[r.Name] {
			return DuplicateModule(r.Name)
		}
		seen[r.Name] = true
	}
	commands := map[string]bool{}
	for _, r := range s.regs {
		if r.Core && (len(r.Needs) > 0 || len(r.Requires) > 0) {
			return &InvalidCore{Module: r.Name}
		}
		for _, dep := range r.Needs {
			if !seen[dep] {
				return &UnknownDependency{Module: r.Name, Dependency: dep}
			}
		}
		if !r.Core {
			if _, ok := s.reg.Lookup(FlagKey(r.Name)); !ok {
				return FlagNotRegistered(FlagKey(r.Name))
			}
		}
		for _, rq := range r.Requires {
			if rq.Key == "" {
				continue
			}
			if _, ok := s.reg.Lookup(rq.Key); !ok {
				return settings.UnknownKey(rq.Key)
			}
		}
		for _, c := range r.Commands {
			if commands[c.Name] {
				return DuplicateCommand(c.Name)
			}
			commands[c.Name] = true
		}
		for _, p := range r.Pages {
			if p.Module == nil && p.Start == nil && len(p.Globals) == 0 && p.Templates == nil {
				return &InvalidPage{Module: r.Name}
			}
		}
	}
	for _, r := range s.regs {
		for path, leaf := range r.Leaves {
			if !hasPath(r.Commands, strings.Fields(path)) {
				return &UnknownLeaf{Module: r.Name, Path: path}
			}
			if leaf.Always && len(leaf.Needs) > 0 {
				return &InvalidLeaf{Module: r.Name, Path: path}
			}
			for _, name := range leaf.Needs {
				if !seen[name] {
					return &UnknownDependency{Module: r.Name, Dependency: name}
				}
			}
		}
	}
	return nil
}

func hasPath(cmds []*cli.Command, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, c := range cmds {
		if c.Name != path[0] {
			continue
		}
		if len(path) == 1 {
			return true
		}
		return hasPath(c.Commands, path[1:])
	}
	return false
}

type Snapshot struct {
	set      *Set
	statuses []Status
	once     sync.Once
	registry *registry.Registry
	regErr   error
}

func (s *Set) Current() *Snapshot { return s.cur.Load() }

func (s *Set) Evaluate(ctx context.Context, q db.Querier) (*Snapshot, error) {
	probe := Probe{Settings: s.reg, Q: q, Env: s.env}
	statuses, _, err := evaluate(ctx, s.regs, func(ctx context.Context, r Registration, states map[string]State) ([]Unmet, bool, error) {
		unmet, err := s.unmet(ctx, probe, r, states)
		if err != nil {
			return nil, false, err
		}
		found, err := s.reg.Resolve(ctx, q, FlagKey(r.Name))
		if err != nil {
			return nil, false, err
		}
		return unmet, found.Value == "true", nil
	})
	if err != nil {
		return nil, err
	}
	return &Snapshot{set: s, statuses: statuses}, nil
}

type check func(ctx context.Context, r Registration, states map[string]State) ([]Unmet, bool, error)

func evaluate(ctx context.Context, regs []Registration, fn check) ([]Status, map[string]State, error) {
	states := map[string]State{}
	statuses := make([]Status, 0, len(regs))
	for _, r := range regs {
		st := Status{Name: r.Name, State: Enabled}
		if !r.Core {
			unmet, on, err := fn(ctx, r, states)
			if err != nil {
				return nil, nil, err
			}
			st.Unmet = unmet
			switch {
			case len(unmet) > 0:
				st.State = NeedsSettings
			case !on:
				st.State = Off
			}
		}
		states[r.Name] = st.State
		statuses = append(statuses, st)
	}
	return statuses, states, nil
}

func (s *Set) coreOnly() *Snapshot {
	statuses, _, _ := evaluate(context.Background(), s.regs, func(context.Context, Registration, map[string]State) ([]Unmet, bool, error) {
		return nil, false, nil
	})
	return &Snapshot{set: s, statuses: statuses}
}

func (s Status) Reason() string { return joinReasons(s.Unmet) }

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
	for _, r := range n.set.regs {
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

func (n *Snapshot) Registry() (*registry.Registry, error) {
	n.once.Do(func() { n.registry, n.regErr = n.buildRegistry() })
	return n.registry, n.regErr
}

func (n *Snapshot) buildRegistry() (*registry.Registry, error) {
	out := registry.New()
	for _, r := range n.set.regs {
		if r.Tools == nil || !n.Enabled(r.Name) {
			continue
		}
		if err := r.Tools(out); err != nil {
			return nil, err
		}
	}
	if _, err := out.All(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Set) Guard(ctx context.Context, q db.Querier, key, value string) error {
	for _, r := range s.regs {
		if key != FlagKey(r.Name) || value != "true" {
			continue
		}
		snap, err := s.Evaluate(ctx, q)
		if err != nil {
			return err
		}
		if st, _ := snap.Status(r.Name); len(st.Unmet) > 0 {
			return &NeedsSettingsError{Module: r.Name, Unmet: st.Unmet}
		}
	}
	return nil
}
