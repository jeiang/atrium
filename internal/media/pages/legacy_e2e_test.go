package pages

import (
	"net/http"
	"strings"
	"testing"
)

func TestLegacyMediaPathsRedirect(t *testing.T) {
	w := flNew(t, nil, Options{})
	moved := func(label, target, want string, code int, method string) {
		t.Helper()
		rec := w.do(method, target, nil)
		status(t, rec, code, label)
		if got := rec.Header().Get("Location"); got != want {
			t.Fatalf("%s: Location %q, want %q", label, got, want)
		}
	}
	moved("query kept", "/media/library?x=1", "/apps/media/library?x=1", http.StatusMovedPermanently, http.MethodGet)
	moved("root", "/media", "/apps/media", http.StatusMovedPermanently, http.MethodGet)
	moved("escaped path", "/media/library/movie/Dune%20%282021%29", "/apps/media/library/movie/Dune%20%282021%29", http.StatusMovedPermanently, http.MethodGet)
	moved("post keeps method", "/media/channels/poll", "/apps/media/channels/poll", http.StatusPermanentRedirect, http.MethodPost)
	status(t, w.get("/apps/media/library"), http.StatusOK, "new path")

	proxy := flProxy(t, w.site)
	good := flSigned(flKey, w.movieRel, flFar)
	old := strings.TrimPrefix(good, "/apps")
	rec := flGetHost(proxy, old, host)
	status(t, rec, http.StatusMovedPermanently, "old signed link without group")
	if got := rec.Header().Get("Location"); got != good {
		t.Fatalf("old signed link: Location %q, want %q", got, good)
	}
	status(t, flGetHost(proxy, good, host), http.StatusOK, "followed signed link")
	status(t, flGetHost(proxy, "/media/downloads", host), http.StatusForbidden, "old page without group")
}
