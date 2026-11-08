package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRouteAccessClasses(t *testing.T) {
	srv, err := New(Options{
		Auth:         []string{AuthProxy},
		AllowedHosts: []string{"example.com"},
		Modules: []Module{func(r chi.Router, d *Deps) {
			d.Public(http.MethodGet, "/public", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			d.Admin(http.MethodGet, "/admin", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			r.Get("/settings/example", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, groups string) int {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		if groups != "" {
			req.Header.Set("Remote-Groups", groups)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := request("/public", ""); got != http.StatusNoContent {
		t.Errorf("public route: %d", got)
	}
	if got := request("/admin", ""); got != http.StatusForbidden {
		t.Errorf("anonymous admin route: %d", got)
	}
	if got := request("/admin", Group); got != http.StatusForbidden {
		t.Errorf("non-admin admin route: %d", got)
	}
	if got := request("/admin", Group+",atrium_admin"); got != http.StatusNoContent {
		t.Errorf("admin route: %d", got)
	}
	if got := request("/settings/example", Group); got != http.StatusForbidden {
		t.Errorf("settings route: %d", got)
	}
}
