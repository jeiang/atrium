package about

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/web"
)

//go:embed templates/*.gohtml THIRD_PARTY_NOTICES
var assets embed.FS

func Templates() fs.FS {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	return files
}

func Module(version string) web.Module {
	return func(r chi.Router, d *web.Deps) {
		r.Get("/about", func(w http.ResponseWriter, r *http.Request) {
			f, err := d.Render.Frame(r, "about", struct {
				Version string `json:"version"`
				Notices string `json:"notices"`
			}{version, string(mustNotices())})
			if err != nil {
				panic(err)
			}
			if err := d.Render.Page(w, r, http.StatusOK, "about.gohtml", f); err != nil {
				panic(err)
			}
		})
	}
}

func mustNotices() []byte {
	data, err := assets.ReadFile("THIRD_PARTY_NOTICES")
	if err != nil {
		panic(err)
	}
	return data
}
