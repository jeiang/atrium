package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var log = logx.Logger("atrium.web")

type ErrorPage func(w http.ResponseWriter, r *http.Request, status int, problem string)

type access uint8

const (
	accessDefault access = iota
	accessPublic
)

type routeRule struct {
	method  string
	pattern string
	access  access
	owner   string
}

type Provided struct {
	ResumeRun func(ctx context.Context, runID int64) error
}

type SettingsRender func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame))

type Deps struct {
	Ctx      context.Context
	Cfg      config.Config
	Env      config.Env
	DB       *db.DB
	Settings *settings.Registry
	State    *config.State
	Account  *authapp.Service
	Render   *Renderer
	Error    func(w http.ResponseWriter, r *http.Request, status int, detail string)
	NotFound http.HandlerFunc
	OnFrame  func(FrameFunc)
	Stopping <-chan struct{}
	Provided

	settingsPages map[string]SettingsRender

	commands      func(pattern string, next http.Handler) http.Handler
	router        chi.Router
	routes        *[]routeRule
	owner         string
	registrations *registrations
	coreRoutes    func(*Deps)
	onClose       func(func() error)
}

const drainTimeout = 15 * time.Second

func (d *Deps) DrainOnClose(drain func(context.Context) error) {
	if d.onClose == nil {
		return
	}
	d.onClose(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		if err := drain(ctx); err != nil {
			return fmt.Errorf("could not drain work before closing the database: %w", err)
		}
		return nil
	})
}

func (d *Deps) SettingsAt(url string, render SettingsRender) {
	d.settingsPages[url] = render
}

func (d *Deps) SettingsPage(w http.ResponseWriter, r *http.Request, status int, url string, mutate func(*tpl.Frame)) bool {
	render, ok := d.settingsPages[url]
	if !ok {
		return false
	}
	at := r.Clone(r.Context())
	at.URL.Path, at.URL.RawQuery = url, ""
	render(w, at, status, mutate)
	return true
}

func (d *Deps) Public(method, pattern string, h http.HandlerFunc) {
	d.route(method, pattern, accessPublic, h)
}

func (d *Deps) Authenticated(method, pattern string, h http.HandlerFunc) {
	d.route(method, pattern, accessDefault, h)
}

func (d *Deps) route(method, pattern string, class access, h http.HandlerFunc) {
	d.router.Method(method, pattern, h)
	*d.routes = append(*d.routes, routeRule{method: method, pattern: pattern, access: class, owner: d.owner})
}

type Module func(r chi.Router, d *Deps)

type Options struct {
	Ctx          context.Context
	Cfg          config.Config
	Env          config.Env
	DB           *db.DB
	Settings     *settings.Registry
	State        *config.State
	Auth         []string
	AllowedHosts []string
	Trusted      []netip.Prefix
	Public       func(*http.Request) bool
	Render       *Renderer
	Modules      []Module
	Home         string
	OIDC         *oidc.Service
	Policy       *Policy
	OnReady      func(*Deps) error
	Bindings     map[string][]application.Binding
	Commands     func(pattern string, next http.Handler) http.Handler
}

type Server struct {
	base          context.Context
	auth          Policy
	oidc          *oidc.Service
	bearers       *bearer.Service
	account       *authapp.Service
	db            *db.DB
	render        *Renderer
	secure        func(*http.Request) bool
	handler       http.Handler
	mux           *chi.Mux
	errPage       ErrorPage
	stop          chan struct{}
	stopOnce      sync.Once
	starting      atomic.Bool
	closers       []func() error
	routes        []routeRule
	registrations *registrations
}

func New(o Options) (*Server, error) {
	var auth Policy
	if o.Policy != nil {
		auth = *o.Policy
	} else {
		var err error
		auth, err = parseAuth(o.Auth)
		if err != nil {
			return nil, err
		}
	}
	if o.Env == nil {
		o.Env = config.OS
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.Settings != nil {
		o.Ctx = settings.With(o.Ctx, o.Settings)
	}
	if o.State != nil {
		o.Ctx = config.With(o.Ctx, o.State)
	}
	if auth.OIDC() && (o.OIDC == nil || o.Render == nil || o.DB == nil) {
		return nil, errors.New("oidc auth needs a session service, a renderer and a database")
	}
	s := &Server{mux: chi.NewRouter(), oidc: o.OIDC, render: o.Render, auth: auth, base: o.Ctx, db: o.DB}
	s.registrations = &registrations{seen: map[routeKey]string{}, bindings: o.Bindings}
	core := &Registrar{Router: s.mux, owner: "core", state: s.registrations}
	if o.DB != nil {
		s.bearers = bearer.New(o.DB, o.OIDC)
		s.account = authapp.New(o.DB, o.OIDC, s.bearers)
	}
	s.mux.Use(instrument)
	if s.errPage == nil && o.Render != nil {
		s.errPage = o.Render.ErrorPage
	}
	if s.errPage == nil {
		s.errPage = plainErrorPage
	}
	static := newStaticHandler(Static, Assets)
	s.mux.NotFound(s.notFound)
	s.mux.MethodNotAllowed(s.methodNotAllowed)
	core.Get(healthPath, func(w http.ResponseWriter, _ *http.Request) {
		if s.starting.Load() || s.stopped() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	core.Handle(staticPrefix+"*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		static.serve(w, r, func(status int, detail string) { s.Error(w, r, status, detail) })
	}))
	home := o.Home
	if home == "" {
		home = "/about"
	}
	core.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, home, http.StatusSeeOther)
	})
	s.stop = make(chan struct{})
	deps := &Deps{
		Ctx: o.Ctx, Cfg: o.Cfg, Env: o.Env, DB: o.DB, Settings: o.Settings, State: o.State, Account: s.account, Render: o.Render, Error: s.Error, NotFound: s.missing, Stopping: s.stop,
		OnFrame: func(FrameFunc) {}, settingsPages: map[string]SettingsRender{},
		onClose:  func(f func() error) { s.closers = append(s.closers, f) },
		commands: o.Commands, router: core, routes: &s.routes, owner: "core", registrations: s.registrations,
	}
	if o.Render != nil {
		deps.OnFrame = o.Render.OnFrame
	}
	deps.coreRoutes = func(d *Deps) {
		d.Authenticated(http.MethodGet, authSettingsPath, s.signInPage)
		d.Authenticated(http.MethodPost, authSettingsPath+"/tokens", s.tokenIssue)
		d.Authenticated(http.MethodPost, authSettingsPath+"/tokens/{id}/revoke", s.tokenRevoke)
	}
	s.secure = func(r *http.Request) bool {
		return hasSecureTransport(r, strings.HasPrefix(o.State.PublicURL(o.Env), "https://"))
	}
	if auth.OIDC() {
		o.Render.oidcOn.Store(true)
		s.registerAuthRoutes()
	}
	if o.OnReady != nil {
		if err := o.OnReady(deps); err != nil {
			return nil, errors.Join(err, s.Close())
		}
	}
	for _, register := range o.Modules {
		register(s.mux, deps)
	}
	if err := (&Registrar{state: deps.registrations}).Seal(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	guard := newGuard(auth, o.AllowedHosts, o.Trusted, o.Public, s.access)
	guard.secure = s.secure
	guard.sessions = o.OIDC
	guard.bearers = s.bearers
	guard.errPage = s.errPage
	if o.DB != nil {
		guard.bodyCap = func(context.Context) (int, error) {
			return config.TuningBodyCapBytes, nil
		}
	}
	s.handler = otelhttp.NewHandler(guard.Middleware(Gzip(recoverer(s.mux))), "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return !strings.HasPrefix(r.URL.Path, staticPrefix) && r.URL.Path != healthPath
		}),
	)
	return s, nil
}

func (s *Server) Unbound() []Route { return s.registrations.unbound() }

func Unbound(ctx context.Context, pages []Page) ([]Route, error) {
	var modules []Module
	bindings := map[string][]application.Binding{}
	for _, p := range pages {
		if p.Module != nil {
			modules = append(modules, p.Module)
		}
		maps.Copy(bindings, p.Bindings)
	}
	srv, err := New(Options{Ctx: ctx, Modules: modules, Bindings: bindings})
	if err != nil {
		return nil, err
	}
	unbound := srv.Unbound()
	if err := srv.Close(); err != nil {
		return nil, err
	}
	return unbound, nil
}

func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics := httpsnoop.CaptureMetrics(next, w, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(metrics.Code)
		obs.HTTPRequests.Add(1, route, r.Method, status)
		obs.HTTPDuration.Observe(metrics.Duration.Seconds(), route, r.Method, status)
		span := trace.SpanFromContext(r.Context())
		if span.IsRecording() {
			span.SetName(r.Method + " " + route)
			span.SetAttributes(attribute.String(obs.HTTPRoute, route))
		}
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r.WithContext(baseValues{Context: r.Context(), base: s.base}))
}

type baseValues struct {
	context.Context
	base context.Context
}

func (b baseValues) Value(key any) any {
	if v := b.Context.Value(key); v != nil {
		return v
	}
	return b.base.Value(key)
}

func (s *Server) route(class access, method, pattern string, h http.HandlerFunc) {
	(&Registrar{Router: s.mux, owner: "core", state: s.registrations}).Method(method, pattern, h)
	s.routes = append(s.routes, routeRule{method: method, pattern: pattern, access: class, owner: "core"})
}

func (s *Server) access(r *http.Request) access {
	ctx := chi.NewRouteContext()
	if !s.mux.Match(ctx, r.Method, r.URL.Path) {
		return accessDefault
	}
	pattern := ctx.RoutePattern()
	for _, rule := range s.routes {
		if rule.method == r.Method && rule.pattern == pattern {
			return rule.access
		}
	}
	return accessDefault
}

func (s *Server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Server) Hold() { s.starting.Store(true) }

func (s *Server) Release() { s.starting.Store(false) }

func (s *Server) StopStreams() { s.stopOnce.Do(func() { close(s.stop) }) }

func (s *Server) Close() error {
	s.StopStreams()
	var err error
	for i := len(s.closers) - 1; i >= 0; i-- {
		err = errors.Join(err, s.closers[i]())
	}
	s.closers = nil
	return err
}

func IsPartial(r *http.Request) bool {
	_, hx := r.Header["Hx-Request"]
	_, restore := r.Header["Hx-History-Restore-Request"]
	return hx && !restore
}

var methods = [...]string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPost, http.MethodPut}

func (s *Server) allowed(path string) []string {
	var allow []string
	for _, m := range methods {
		if s.mux.Match(chi.NewRouteContext(), m, path) {
			allow = append(allow, m)
		}
	}
	return allow
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	alt := path + "/"
	if strings.HasSuffix(path, "/") {
		alt = strings.TrimRight(path, "/")
	}
	if isAPI(r) || alt == "" || alt == path || len(s.allowed(alt)) == 0 {
		s.missing(w, r)
		return
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	target := url.URL{Scheme: scheme, Host: r.Host, Path: alt, RawQuery: r.URL.RawQuery}
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func (s *Server) missing(w http.ResponseWriter, r *http.Request) {
	if isAPI(r) {
		APIError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	s.Error(w, r, http.StatusNotFound, "Not Found")
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if allow := s.allowed(r.URL.Path); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
	}
	if isAPI(r) {
		APIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		return
	}
	s.Error(w, r, http.StatusMethodNotAllowed, "Method Not Allowed")
}

func (s *Server) Error(w http.ResponseWriter, r *http.Request, status int, detail string) {
	if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		s.errPage(w, r, status, problem(detail))
		return
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"detail": detail})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(body.Bytes(), "\n"))
}

func problem(detail string) string {
	if detail == "" {
		return "."
	}
	first, size := utf8.DecodeRuneInString(detail)
	return strings.ToUpper(string(first)) + detail[size:] + "."
}

func plainErrorPage(w http.ResponseWriter, _ *http.Request, status int, problem string) {
	deny(w, status, problem)
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			h := w.Header()
			for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Set-Cookie"} {
				h.Del(name)
			}
			log.Error("unhandled exception", logx.Err, fmt.Sprint(rec), "method", r.Method, "path", r.URL.Path)
			if isAPI(r) {
				APIError(w, http.StatusInternalServerError, "internal", "Internal error.")
				return
			}
			deny(w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(w, r)
	})
}
