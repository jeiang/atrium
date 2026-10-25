package module

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/assistant/registry"
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

func NewSet(reg *settings.Registry, env config.Env, mods ...Module) (*Set, error) {
	s := &Set{reg: reg, env: env}
	var regs []Registration
	for _, m := range mods {
		regs = append(regs, m.Register(Context{Current: s.Current}))
	}
	s.regs = byNeeds(regs)
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func byNeeds(regs []Registration) []Registration {
	placed := map[string]bool{}
	var out []Registration
	for len(regs) > 0 {
		i := slices.IndexFunc(regs, func(r Registration) bool {
			return !slices.ContainsFunc(r.Needs, func(dep string) bool { return !placed[dep] })
		})
		if i < 0 {
			return append(out, regs...)
		}
		placed[regs[i].Name] = true
		out = append(out, regs[i])
		regs = slices.Delete(regs, i, i+1)
	}
	return out
}

func (s *Set) validate() error {
	seen := map[string]bool{}
	commands := map[string]bool{}
	for _, r := range s.regs {
		if r.Name == "" || seen[r.Name] {
			return DuplicateModule(r.Name)
		}
		if r.Core && (len(r.Needs) > 0 || len(r.Requires) > 0) {
			return &InvalidCore{Module: r.Name}
		}
		for _, dep := range r.Needs {
			if !seen[dep] {
				return &UnknownDependency{Module: r.Name, Dependency: dep}
			}
		}
		seen[r.Name] = true
		if !r.Core {
			if _, ok := s.reg.Lookup(FlagKey(r.Name)); !ok {
				return FlagNotRegistered(FlagKey(r.Name))
			}
		}
		for _, rq := range r.Requires {
			if _, ok := s.reg.Lookup(rq.Key); rq.Key != "" && !ok {
				return settings.UnknownKey(rq.Key)
			}
		}
		for _, c := range r.Commands {
			if commands[c.Name] {
				return DuplicateCommand(c.Name)
			}
			commands[c.Name] = true
		}
	}
	for _, r := range s.regs {
		for _, name := range r.Soft {
			if !seen[name] {
				return &UnknownDependency{Module: r.Name, Dependency: name}
			}
		}
		for path, leaf := range r.Leaves {
			if !hasPath(r.Commands, strings.Fields(path)) {
				return &UnknownLeaf{Module: r.Name, Path: path}
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
}

func (s *Set) Current() *Snapshot { return s.cur.Load() }

func (s *Set) Evaluate(ctx context.Context, q db.Querier) (*Snapshot, error) {
	snap := &Snapshot{set: s}
	states := map[string]State{}
	probe := Probe{Settings: s.reg, Q: q, Env: s.env}
	for _, r := range s.regs {
		st := Status{Name: r.Name, State: Enabled}
		if !r.Core {
			unmet, err := s.unmet(ctx, probe, r, states)
			if err != nil {
				return nil, err
			}
			st.Unmet = unmet
			on, err := s.reg.Resolve(ctx, q, FlagKey(r.Name), 0)
			if err != nil {
				return nil, err
			}
			switch {
			case len(st.Unmet) > 0:
				st.State, st.Reason = NeedsSettings, joinReasons(st.Unmet)
			case on.Value != "true":
				st.State = Off
			}
		}
		states[r.Name] = st.State
		snap.statuses = append(snap.statuses, st)
	}
	return snap, nil
}

func (s *Set) coreOnly() *Snapshot {
	snap := &Snapshot{set: s}
	for _, r := range s.regs {
		st := Status{Name: r.Name, State: Off}
		if r.Core {
			st.State = Enabled
		}
		snap.statuses = append(snap.statuses, st)
	}
	return snap
}

func (n *Snapshot) Statuses() []Status { return slices.Clone(n.statuses) }

func (n *Snapshot) Status(name string) (Status, bool) {
	for _, st := range n.statuses {
		if st.Name == name {
			return st, true
		}
	}
	return Status{}, false
}

func (n *Snapshot) Enabled(name string) bool {
	st, ok := n.Status(name)
	return ok && st.State == Enabled
}

func (n *Snapshot) Registry() (*registry.Registry, error) {
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
