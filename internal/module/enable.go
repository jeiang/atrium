package module

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/web"
)

const (
	HoldOff         = "off"
	HoldPinned      = "pinned"
	HoldNeeds       = string(NeedsSettings)
	HoldUnavailable = string(Unavailable)
)

type Hold struct {
	Module string `json:"module"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

func (s *Set) flagged(name string) bool {
	for _, r := range s.decls {
		if r.Name == name {
			return !r.Core
		}
	}
	return false
}

func (s *Set) Held(ctx context.Context, q db.Querier, name string) (Hold, error) {
	if !s.flagged(name) {
		return Hold{}, nil
	}
	h := Hold{Module: name}
	found, err := s.reg.Resolve(ctx, q, FlagKey(name))
	if err != nil || found.Value == "true" {
		return h, err
	}
	source, pinned, err := s.reg.Pinned(FlagKey(name))
	if err != nil {
		return Hold{}, err
	}
	if pinned {
		h.Result, h.Detail = HoldPinned, source
		return h, nil
	}
	err = s.Guard(ctx, q, FlagKey(name), "true")
	var needs *NeedsSettingsError
	var unavailable *UnavailableError
	switch {
	case errors.As(err, &needs):
		h.Result, h.Detail = HoldNeeds, joinReasons(needs.Unmet)
		return h, nil
	case errors.As(err, &unavailable):
		h.Result, h.Detail = HoldUnavailable, unavailable.Err
		return h, nil
	}
	if err != nil {
		return Hold{}, err
	}
	h.Result = HoldOff
	return h, nil
}

type AppLink struct {
	ID      string
	Name    string
	Enabled bool
}

type App struct {
	ID         string
	Name       string
	Enabled    bool
	State      State
	Reason     string
	PinSource  string
	Depends    []AppLink
	Dependents []AppLink
	Nav        []web.NavItem
}

type UnknownApp struct{ ID string }

func (e *UnknownApp) Error() string { return "no app is named " + e.ID }

const (
	RefusedCore       = "core_unit"
	RefusedPinned     = "pinned"
	RefusedDependency = "dependency_off"
	RefusedDependent  = "dependent_on"
)

type SwitchRefused struct {
	App     string
	Reason  string
	Source  string
	Modules []string
}

func (e *SwitchRefused) Error() string {
	switch e.Reason {
	case RefusedCore:
		return e.App + " is a core unit, not an app the operator switches"
	case RefusedPinned:
		return e.App + " is pinned by " + e.Source + "; change it in the host configuration"
	case RefusedDependency:
		return e.App + " needs " + strings.Join(e.Modules, ", ") + " on first"
	}
	return strings.Join(e.Modules, ", ") + " needs " + e.App + "; turn it off first"
}

func (s *Set) declaration(id string) (Declaration, bool) {
	i := slices.IndexFunc(s.decls, func(d Declaration) bool { return d.Name == id })
	if i < 0 {
		return Declaration{}, false
	}
	return s.decls[i], true
}

func (s *Set) flagOn(ctx context.Context, q db.Querier, name string) (bool, error) {
	d, ok := s.declaration(name)
	if !ok {
		return false, nil
	}
	if d.Core {
		return true, nil
	}
	found, err := s.reg.Resolve(ctx, q, FlagKey(name))
	return found.Value == "true", err
}

func (s *Set) link(ctx context.Context, q db.Querier, name string) (AppLink, error) {
	d, _ := s.declaration(name)
	on, err := s.flagOn(ctx, q, name)
	return AppLink{ID: name, Name: cmp.Or(d.Title, name), Enabled: on}, err
}

func (s *Set) Apps(ctx context.Context, q db.Querier) ([]App, error) {
	var out []App
	for _, d := range s.decls {
		if !d.App {
			continue
		}
		app, err := s.App(ctx, q, d.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	slices.SortFunc(out, func(a, b App) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *Set) App(ctx context.Context, q db.Querier, id string) (App, error) {
	d, ok := s.declaration(id)
	if !ok {
		return App{}, &UnknownApp{ID: id}
	}
	if !d.App {
		return App{}, &SwitchRefused{App: id, Reason: RefusedCore}
	}
	on, err := s.flagOn(ctx, q, id)
	if err != nil {
		return App{}, err
	}
	app := App{ID: id, Name: cmp.Or(d.Title, id), Enabled: on, State: Off, Depends: []AppLink{}, Dependents: []AppLink{}, Nav: []web.NavItem{}}
	if snap := s.Current(); snap != nil {
		if st, ok := snap.Status(id); ok {
			app.State, app.Reason = st.State, st.Reason()
		}
	}
	if app.PinSource, _, err = s.reg.Pinned(FlagKey(id)); err != nil {
		return App{}, err
	}
	for _, dep := range d.Depends {
		l, err := s.link(ctx, q, dep)
		if err != nil {
			return App{}, err
		}
		app.Depends = append(app.Depends, l)
	}
	for _, other := range s.decls {
		if slices.Contains(other.Depends, id) {
			l, err := s.link(ctx, q, other.Name)
			if err != nil {
				return App{}, err
			}
			app.Dependents = append(app.Dependents, l)
		}
	}
	for _, p := range d.Pages {
		for _, item := range p.Nav {
			if !item.Hub && !item.Brand && !item.Footer {
				app.Nav = append(app.Nav, item)
			}
		}
	}
	slices.SortStableFunc(app.Nav, func(a, b web.NavItem) int { return cmp.Compare(a.Order, b.Order) })
	return app, nil
}

func (s *Set) CheckSwitch(ctx context.Context, q db.Querier, id string, on bool) error {
	app, err := s.App(ctx, q, id)
	if err != nil {
		return err
	}
	if app.PinSource != "" {
		return &SwitchRefused{App: id, Reason: RefusedPinned, Source: app.PinSource}
	}
	links, reason := app.Dependents, RefusedDependent
	if on {
		links, reason = app.Depends, RefusedDependency
	}
	var blocking []string
	for _, l := range links {
		if l.Enabled != on {
			blocking = append(blocking, l.ID)
		}
	}
	if len(blocking) > 0 {
		return &SwitchRefused{App: id, Reason: reason, Modules: blocking}
	}
	return nil
}

func (n *Snapshot) AppsOff() []AppLink {
	var out []AppLink
	for _, d := range n.set.decls {
		if st, _ := n.Status(d.Name); d.App && st.State == Off {
			out = append(out, AppLink{ID: d.Name, Name: cmp.Or(d.Title, d.Name)})
		}
	}
	slices.SortFunc(out, func(a, b AppLink) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (s *Set) Follow(ctx context.Context, q db.Querier) (bool, error) {
	s.publish.Lock()
	defer s.publish.Unlock()
	snap, err := s.Evaluate(ctx, q)
	if err != nil {
		return false, err
	}
	if cur := s.Current(); cur != nil && slices.EqualFunc(cur.statuses, snap.statuses, func(a, b Status) bool {
		return a.Name == b.Name && a.State == b.State && a.Err == b.Err && a.Reason() == b.Reason()
	}) {
		return false, nil
	}
	s.store(snap)
	return true, nil
}
