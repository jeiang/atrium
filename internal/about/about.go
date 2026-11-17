package about

import (
	_ "embed"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/web"
)

const NoticesPath = "/third-party-notices.txt"

//go:embed THIRD_PARTY_NOTICES
var thirdPartyNotices string

func Module(_ chi.Router, d *web.Deps) {
	d.Authenticated(http.MethodGet, NoticesPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(thirdPartyNotices))
	})
}
