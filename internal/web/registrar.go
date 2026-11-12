package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
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

type routeKey struct{ method, pattern string }
type registrations struct {
	seen   map[routeKey]string
	sealed bool
	err    error
}

type Registrar struct {
	chi.Router
	owner  string
	prefix string
	state  *registrations
}

func (r *Registrar) Seal() error { r.state.sealed = true; return r.state.err }

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
