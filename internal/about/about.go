package about

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/web"
)

//go:embed templates/*.gohtml
var assets embed.FS

//go:embed THIRD_PARTY_NOTICES
var thirdPartyNotices string

func Templates() fs.FS {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	return files
}

func noticeCount(text string) int {
	lines := strings.Split(text, "\n")
	separator := strings.Repeat("=", 64)
	n := 0
	for i, line := range lines {
		if line != separator || i+1 >= len(lines) {
			continue
		}
		if strings.Contains(lines[i+1], " (") && !strings.HasPrefix(lines[i+1], " ") {
			n++
		}
	}
	return n
}

func Module(version string) web.Module {
	return func(r chi.Router, d *web.Deps) {
		r.Get("/about", func(w http.ResponseWriter, r *http.Request) {
			f, err := d.Render.Frame(r, "about", struct {
				Version     string `json:"version"`
				NoticeCount int    `json:"notice_count"`
				Notices     string `json:"notices"`
			}{version, strings.Count(thirdPartyNotices, "================================================================\n") / 2, thirdPartyNotices})
			if err != nil {
				panic(err)
			}
			if err := d.Render.Page(w, r, http.StatusOK, "about.gohtml", f); err != nil {
				panic(err)
			}
		})
	}
}
