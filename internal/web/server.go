// Package web is the HTTP server: the Guard security layer, static assets,
// error handling and the registry of page modules.
package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.web")

// ErrorPage renders the 404 and 422 error page; problem is the capitalised
// detail with a trailing period.
type ErrorPage func(w http.ResponseWriter, r *http.Request, status int, problem string)

// Deps is what a Module registers its routes with.
type Deps struct {
	Cfg config.Config
	Env config.Env
	DB  *db.DB
	// Error answers with the app's error handling: the error page for 404 and
	// 422, a JSON {"detail": ...} body for every other status.
	Error func(w http.ResponseWriter, r *http.Request, status int, detail string)
}

// Module registers one page set's routes on the shared router, in the order
// the Python app registered them: a static path before its {id} sibling.
type Module func(r chi.Router, d *Deps)

type Options struct {
	Cfg config.Config
	Env config.Env
	DB  *db.DB
	// Auth is AuthProxy or AuthLocal.
	Auth         string
	AllowedHosts []string
	// Public admits a request without Remote-Groups, see NewGuard.
	Public    func(*http.Request) bool
	ErrorPage ErrorPage
	Static    fs.FS
	Modules   []Module
}

type Server struct {
	handler http.Handler
	mux     *chi.Mux
	errPage ErrorPage
}

func New(o Options) (*Server, error) {
	if o.Auth != AuthProxy && o.Auth != AuthLocal {
		return nil, fmt.Errorf("unknown auth mode %q", o.Auth)
	}
	if o.Static == nil {
		o.Static = Static
	}
	if o.Env == nil {
		o.Env = config.OS
	}
	s := &Server{mux: chi.NewRouter(), errPage: o.ErrorPage}
	if s.errPage == nil {
		s.errPage = plainErrorPage
	}
	static := newStaticHandler(o.Static)
	s.mux.NotFound(s.notFound)
	s.mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.Error(w, r, http.StatusMethodNotAllowed, "Method Not Allowed")
	})
	s.mux.Get(HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.Handle(StaticPrefix+"*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		static.serve(w, r, func() { s.Error(w, r, http.StatusNotFound, "Not Found") })
	}))
	s.mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
	})
	deps := &Deps{Cfg: o.Cfg, Env: o.Env, DB: o.DB, Error: s.Error}
	for _, register := range o.Modules {
		register(s.mux, deps)
	}
	guard := NewGuard(o.Auth, o.AllowedHosts, o.Public)
	s.handler = recoverer(guard.Middleware(Gzip(s.mux)))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// IsPartial is deps.is_partial: an htmx request that is not a history restore,
// which wants the fragment rather than the full page.
func IsPartial(r *http.Request) bool {
	_, hx := r.Header["Hx-Request"]
	_, restore := r.Header["Hx-History-Restore-Request"]
	return hx && !restore
}

// notFound redirects with a 307 to the sibling path with or without its
// trailing slash when that one is routed (Starlette's redirect_slashes).
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	alt := path + "/"
	if strings.HasSuffix(path, "/") {
		alt = strings.TrimRight(path, "/")
	}
	if alt != "" && alt != path && s.mux.Match(chi.NewRouteContext(), r.Method, alt) {
		target := url.URL{Path: alt, RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
		return
	}
	s.Error(w, r, http.StatusNotFound, "Not Found")
}

// Error is the app's HTTPException handler: 404 and 422 render the error
// page, every other status answers FastAPI's default {"detail": ...} JSON.
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
			log.Error("unhandled exception", logx.Err, fmt.Sprint(rec), "method", r.Method, "path", r.URL.Path)
			deny(w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(w, r)
	})
}
