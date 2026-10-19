package web

import (
	"net/http"

	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func renderServer(t *testing.T) *client {
	t.Helper()
	cfg := config.Config{Mode: "shadow", DBPath: t.TempDir() + "/atrium.db"}
	render, err := NewRenderer(cfg, nil, tpl.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}, func(o *Options) { o.Cfg = cfg; o.Render = render }))
	c.get("/chat")
	return c
}

func TestNotFoundRendersErrorPageThroughGuard(t *testing.T) {
	c := renderServer(t)
	rec := c.get("/missing")
	wantStatus(t, rec, 404, "missing")
	body := rec.Body.String()
	for _, want := range []string{"<title>Not found · atrium</title>", "Nothing here", `hx-headers='{&#34;X-CSRF-Token&#34;`, "Shadow"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page misses %q", want)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("404 page contains ZgotmplZ")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Error("security headers missing on the error page")
	}
	if !strings.Contains(body, c.token()) || c.token() == "" {
		t.Error("the layout does not carry the Guard's CSRF token")
	}
}

func TestNotFoundPartialRendersFragment(t *testing.T) {
	c := renderServer(t)
	rec := c.get("/missing", [2]string{"HX-Request", "true"})
	wantStatus(t, rec, 404, "partial")
	body := rec.Body.String()
	if !strings.Contains(body, `role="alert"`) || strings.Contains(body, "<html") {
		t.Errorf("partial 404 = %s", body)
	}
}

func TestUnknownRouteAndUnprocessableRenderThePage(t *testing.T) {
	c := renderServer(t)
	rec := c.get("/nowhere")
	wantStatus(t, rec, 404, "unknown route")
	if !strings.Contains(rec.Body.String(), "Nothing here") {
		t.Error("unknown route did not render the error page")
	}
}

func TestUnprocessablePartialCarriesToast(t *testing.T) {
	cfg := config.Config{Mode: "live", DBPath: t.TempDir() + "/atrium.db"}
	render, err := NewRenderer(cfg, nil, tpl.Config{})
	if err != nil {
		t.Fatal(err)
	}
	module := func(r chi.Router, d *Deps) {
		r.Get("/bad", func(w http.ResponseWriter, r *http.Request) {
			d.Error(w, r, http.StatusUnprocessableEntity, `bad <filter> "x"`)
		})
	}
	c := newClient(t, newServer(t, AuthLocal, []string{testHost}, func(o *Options) { o.Render = render; o.Cfg = cfg; o.Modules = []Module{module} }))
	c.get("/bad")
	rec := c.get("/bad", [2]string{"HX-Request", "true"})
	wantStatus(t, rec, 422, "bad partial")
	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="beforeend"`) || !strings.Contains(body, "Filter not understood") || strings.Contains(body, "<filter>") {
		t.Errorf("422 partial = %s", body)
	}
	full := c.get("/bad")
	wantStatus(t, full, 422, "bad page")
	if !strings.Contains(full.Body.String(), "atrium cannot show this") || !strings.Contains(full.Body.String(), `href="/bad"`) {
		t.Errorf("422 page = %s", full.Body.String())
	}
}
