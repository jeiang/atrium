package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
)

var ErrRegistrarSealed = errors.New("web: route registrar sealed")

type DuplicateRoute struct {
	Method  string
	Pattern string
	Owners  []string
}

func (e *DuplicateRoute) Error() string {
	return fmt.Sprintf("web: duplicate route %s %s: owners %v", e.Method, e.Pattern, e.Owners)
}

type UnmountedBinding struct {
	Owner  string
	Kind   application.BindingKind
	Target string
}

func (e *UnmountedBinding) Error() string {
	return fmt.Sprintf("web: %s declares %s binding %s but does not mount it", e.Owner, e.Kind, e.Target)
}

type Route struct {
	Owner   string
	Kind    application.BindingKind
	Method  string
	Pattern string
}

type routeKey struct{ method, pattern string }
type registrations struct {
	seen     map[routeKey]string
	bindings map[string][]application.Binding
	bound    map[routeKey]bool
	sealed   bool
	err      error
}

func (s *registrations) check() error {
	s.bound = map[routeKey]bool{}
	for owner, bindings := range s.bindings {
		for _, b := range bindings {
			method, pattern, ok := application.Route(b.Target)
			if b.Kind == application.API {
				pattern = APIPrefix + pattern
			}
			key := routeKey{method, pattern}
			if !ok || s.seen[key] != owner {
				return &UnmountedBinding{Owner: owner, Kind: b.Kind, Target: b.Target}
			}
			s.bound[key] = true
		}
	}
	return nil
}

func (s *registrations) unbound() []Route {
	var out []Route
	for key, owner := range s.seen {
		if s.bound[key] {
			continue
		}
		route := Route{Owner: owner, Kind: application.HTML, Method: key.method, Pattern: key.pattern}
		if rest, ok := strings.CutPrefix(key.pattern, APIPrefix); ok && strings.HasPrefix(rest, "/") {
			route.Kind, route.Pattern = application.API, rest
		}
		out = append(out, route)
	}
	slices.SortFunc(out, func(a, b Route) int {
		return cmp.Or(cmp.Compare(a.Owner, b.Owner), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Pattern, b.Pattern), cmp.Compare(a.Method, b.Method))
	})
	return out
}

type Registrar struct {
	chi.Router
	owner  string
	prefix string
	state  *registrations
}

func (r *Registrar) Seal() error {
	r.state.sealed = true
	if r.state.err != nil {
		return r.state.err
	}
	return r.state.check()
}

func (r *Registrar) record(method, pattern string) bool {
	if r.state.sealed {
		panic(ErrRegistrarSealed)
	}
	key := routeKey{method, r.prefix + pattern}
	if owner, ok := r.state.seen[key]; ok {
		r.state.err = &DuplicateRoute{Method: method, Pattern: key.pattern, Owners: []string{owner, r.owner}}
		return false
	}
	r.state.seen[key] = r.owner
	return true
}

func (r *Registrar) Method(method, pattern string, h http.Handler) {
	if r.record(method, pattern) {
		r.Router.Method(method, pattern, h)
	}
}
func (r *Registrar) MethodFunc(method, pattern string, h http.HandlerFunc) {
	r.Method(method, pattern, h)
}
func (r *Registrar) Handle(pattern string, h http.Handler) {
	if r.record("*", pattern) {
		r.Router.Handle(pattern, h)
	}
}
func (r *Registrar) HandleFunc(pattern string, h http.HandlerFunc) { r.Handle(pattern, h) }
func (r *Registrar) Get(pattern string, h http.HandlerFunc)        { r.Method(http.MethodGet, pattern, h) }
func (r *Registrar) Post(pattern string, h http.HandlerFunc)       { r.Method(http.MethodPost, pattern, h) }
func (r *Registrar) Put(pattern string, h http.HandlerFunc)        { r.Method(http.MethodPut, pattern, h) }
func (r *Registrar) Patch(pattern string, h http.HandlerFunc)      { r.Method(http.MethodPatch, pattern, h) }
func (r *Registrar) Delete(pattern string, h http.HandlerFunc) {
	r.Method(http.MethodDelete, pattern, h)
}
func (r *Registrar) Head(pattern string, h http.HandlerFunc) { r.Method(http.MethodHead, pattern, h) }
func (r *Registrar) Options(pattern string, h http.HandlerFunc) {
	r.Method(http.MethodOptions, pattern, h)
}
func (r *Registrar) Connect(pattern string, h http.HandlerFunc) {
	r.Method(http.MethodConnect, pattern, h)
}
func (r *Registrar) Trace(pattern string, h http.HandlerFunc) { r.Method(http.MethodTrace, pattern, h) }
func (r *Registrar) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return &Registrar{Router: r.Router.With(middlewares...), owner: r.owner, prefix: r.prefix, state: r.state}
}
func (r *Registrar) Group(fn func(chi.Router)) chi.Router {
	child := &Registrar{owner: r.owner, prefix: r.prefix, state: r.state}
	child.Router = r.Router.Group(func(router chi.Router) {
		child.Router = router
		if fn != nil {
			fn(child)
		}
	})
	return child
}
func (r *Registrar) Route(pattern string, fn func(chi.Router)) chi.Router {
	if r.state.sealed {
		panic(ErrRegistrarSealed)
	}
	child := &Registrar{owner: r.owner, prefix: r.prefix + pattern, state: r.state}
	child.Router = r.Router.Route(pattern, func(router chi.Router) {
		child.Router = router
		fn(child)
	})
	return child
}
func (r *Registrar) Mount(pattern string, h http.Handler) {
	if r.record("MOUNT", pattern) {
		r.Router.Mount(pattern, h)
	}
}

func Owned(owner string, mount Module, middleware func(*Deps, http.Handler) http.Handler) Module {
	return func(router chi.Router, d *Deps) {
		if d.registrations.sealed {
			panic(ErrRegistrarSealed)
		}
		registrar := &Registrar{Router: router, owner: owner, state: d.registrations}
		if middleware != nil {
			registrar.Router = router.With(func(next http.Handler) http.Handler { return middleware(d, next) })
		}
		previousOwner, previousRouter := d.owner, d.router
		d.owner, d.router = owner, registrar
		defer func() { d.owner, d.router = previousOwner, previousRouter }()
		mount(registrar, d)
	}
}

func CoreModule(_ chi.Router, d *Deps) { d.coreRoutes(d) }
