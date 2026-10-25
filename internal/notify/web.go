package notify

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/web"
)

//go:embed templates/*.gohtml
var templates embed.FS

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type listing struct {
	Notifications []Notification `json:"notifications"`
}

func Module(r chi.Router, d *web.Deps) {
	go watchStale(d.DB, d.Env, d.Stopping)
	r.Get("/settings/notifications", func(w http.ResponseWriter, r *http.Request) {
		list, err := Recent(r.Context(), d.DB.Read, 100)
		if err != nil {
			panic(err)
		}
		f, err := d.Render.Frame(r, "settings", listing{list})
		if err != nil {
			panic(err)
		}
		if err := d.Render.Page(w, r, http.StatusOK, "notifications.gohtml", f); err != nil {
			panic(err)
		}
	})
}
