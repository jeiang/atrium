package usagepages

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const URL = "/settings/usage"

//go:embed templates/*.gohtml
var assets embed.FS

func Templates() fs.FS {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	return files
}

func Module() web.Module {
	return func(_ chi.Router, d *web.Deps) {
		page := func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
			f, err := d.Render.Frame(r, "settings", load(r, d, assistant.Now()))
			if err != nil {
				panic(err)
			}
			if mutate != nil {
				mutate(f)
			}
			if err := d.Render.Page(w, r, status, "usage.gohtml", f); err != nil {
				panic(err)
			}
		}
		d.Authenticated(http.MethodGet, URL, func(w http.ResponseWriter, r *http.Request) { page(w, r, http.StatusOK, nil) })
		d.SettingsAt(URL, page)
	}
}

func load(r *http.Request, d *web.Deps, now float64) asstapp.Usage {
	read, err := asstapp.New(d.DB, d.Env, func() float64 { return now }).Usage(r.Context(), asstapp.UsageInput{})
	if err != nil {
		panic(err)
	}
	return read.Data
}
