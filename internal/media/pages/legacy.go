package pages

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

const legacyPrefix = "/media"

func (s *service) registerLegacy(r chi.Router) {
	r.HandleFunc(legacyPrefix, legacyRedirect)
	r.HandleFunc(legacyPrefix+"/*", legacyRedirect)
	r.HandleFunc(legacyPrefix+"/file/*", legacyRedirect)
	s.deps.Public(http.MethodGet, legacyPrefix+"/file/*", legacyRedirect)
	s.deps.Public(http.MethodHead, legacyPrefix+"/file/*", legacyRedirect)
}

func legacyRedirect(w http.ResponseWriter, r *http.Request) {
	target := "/apps" + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	status := http.StatusPermanentRedirect
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		status = http.StatusMovedPermanently
	}
	w.Header().Set("Location", target)
	w.WriteHeader(status)
}
