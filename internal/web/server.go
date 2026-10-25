package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.web")

type ErrorPage func(w http.ResponseWriter, r *http.Request, status int, problem string)

type Deps struct {
	Cfg      config.Config
	Env      config.Env
	DB       *db.DB
	Render   *Renderer
	Error    func(w http.ResponseWriter, r *http.Request, status int, detail string)
	Stopping <-chan struct{}
	OnClose  func(func())
}

type Module func(r chi.Router, d *Deps)

type Options struct {
	Cfg          config.Config
	Env          config.Env
	DB           *db.DB
	Auth         []string
	AllowedHosts []string
	Public       func(*http.Request) bool
	ErrorPage    ErrorPage
	Render       *Renderer
	Static       fs.FS
	Modules      []Module
	OIDC         *oidc.Service
	NoAuth       bool
}

type Server struct {
	oidc     *oidc.Service
	render   *Renderer
	handler  http.Handler
	mux      *chi.Mux
	errPage  ErrorPage
	stop     chan struct{}
	stopOnce sync.Once
	starting atomic.Bool
	closers  []func()
}

func New(o Options) (*Server, error) {
	auth, err := ParseAuth(o.Auth)
	if err != nil {
		return nil, err
	}
	if o.Static == nil {
		o.Static = Static
	}
	if o.Env == nil {
		o.Env = config.OS
	}
	if auth.OIDC && o.OIDC == nil {
		return nil, errors.New("oidc auth needs a session service")
	}
	s := &Server{mux: chi.NewRouter(), errPage: o.ErrorPage, oidc: o.OIDC, render: o.Render}
	if s.errPage == nil && o.Render != nil {
		s.errPage = o.Render.ErrorPage
	}
	if s.errPage == nil {
		s.errPage = plainErrorPage
	}
	static := newStaticHandler(o.Static)
	s.mux.NotFound(s.notFound)
	s.mux.MethodNotAllowed(s.methodNotAllowed)
	s.mux.Get(HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		if s.starting.Load() || s.stopped() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	s.mux.Handle(StaticPrefix+"*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		static.serve(w, r, func(status int, detail string) { s.Error(w, r, status, detail) })
	}))
	s.mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
	})
	s.stop = make(chan struct{})
	deps := &Deps{
		Cfg: o.Cfg, Env: o.Env, DB: o.DB, Render: o.Render, Error: s.Error, Stopping: s.stop,
		OnClose: func(f func()) { s.closers = append(s.closers, f) },
	}
	if auth.OIDC {
		s.registerAuthRoutes(o.Env)
	}
	for _, register := range o.Modules {
		register(s.mux, deps)
	}
	guard := NewGuard(auth, o.AllowedHosts, o.Public)
	guard.sessions = o.OIDC
	guard.errPage = s.errPage
	guard.noAuth = o.NoAuth
	if o.DB != nil {
		guard.admin = func(ctx context.Context) string { return oidc.AdminGroup(ctx, o.DB.Read) }
		guard.bodyCap = func(ctx context.Context) (int, error) { return config.Limit(ctx, o.DB.Read, config.KeyBodyCap) }
	}
	s.handler = guard.Middleware(Gzip(recoverer(s.mux)))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

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

func (s *Server) Close() {
	s.StopStreams()
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	s.closers = nil
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
	if alt == "" || alt == path || len(s.allowed(alt)) == 0 {
		s.Error(w, r, http.StatusNotFound, "Not Found")
		return
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	target := url.URL{Scheme: scheme, Host: r.Host, Path: alt, RawQuery: r.URL.RawQuery}
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if allow := s.allowed(r.URL.Path); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
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
			deny(w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(w, r)
	})
}
