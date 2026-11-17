package packagesmodule

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	pkgapp "github.com/jeiang/atrium/internal/apps/packages/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

func templatesFS() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type pages struct {
	deps *web.Deps
	svc  *pkgapp.Service
}

func htmlRoutes(r chi.Router, d *web.Deps, s *pkgapp.Service) {
	p := &pages{deps: d, svc: s}
	r = r.With(bindPackages)
	r.Get("/watches", p.watchesPage)
	r.Post("/watches/packages/{package_id}/dismiss", p.packageDismiss)
}

func bindPackages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := module.BindOwner(r.Context(), unit.Packages)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type watchesPage struct {
	Packages []pkgapp.Package `json:"packages"`
}

func (p *pages) page(w http.ResponseWriter, r *http.Request, data any, notice *tpl.Toast) {
	f, err := p.deps.Render.Frame(r, "watches", data)
	hub.Check(err)
	f.Toast = notice
	hub.Check(p.deps.Render.Page(w, r, http.StatusOK, "watches.gohtml", f))
}

func (p *pages) showWatches(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	packages, err := p.svc.Packages(r.Context(), pkgapp.PackagesInput{ActiveOnly: true})
	hub.Check(err)
	p.page(w, r, watchesPage{Packages: packages.Data.Packages}, notice)
}

func (p *pages) watchesPage(w http.ResponseWriter, r *http.Request) { p.showWatches(w, r, nil) }

func (p *pages) packageDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := hub.PathID(w, r, "package_id")
	if !ok {
		return
	}
	_, err := p.svc.PackageDismiss(r.Context(), pkgapp.PackageInput{ID: id})
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.NotFound {
		p.deps.Error(w, r, http.StatusNotFound, e.Message)
		return
	}
	hub.Check(err)
	p.showWatches(w, r, hub.Toast("success", "Dismissed"))
}
