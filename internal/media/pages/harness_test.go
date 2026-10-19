package pages

import (
	"context"
	"io"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

const host = "testserver"

var hx = [2]string{"HX-Request", "true"}

type site struct {
	t      *testing.T
	db     *db.DB
	cfg    config.Config
	env    config.Env
	dir    string
	srv    *web.Server
	cookie string
	token  string
}

func newSite(t *testing.T, vars map[string]string, o Options) *site {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	all := map[string]string{"ATRIUM_MODE": "shadow", "ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")}
	for k, v := range vars {
		all[k] = v
	}
	env := config.Map(all)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, path); err != nil {
		t.Fatal(err)
	}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{host}, Public: PublicCheck(cfg, env),
		Render: render, Modules: []web.Module{Module(o)},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &site{t: t, db: d, cfg: cfg, env: env, dir: dir, srv: srv}
	s.handshake()
	return s
}

func (s *site) handshake() {
	rec := s.do("GET", "/media/library", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.CSRFCookie {
			s.cookie, s.token = c.Name+"="+c.Value, c.Value
		}
	}
	if s.token == "" {
		s.t.Fatal("no CSRF cookie issued")
	}
}

func (s *site) do(method, path string, form url.Values, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
		req.Header.Set(web.CSRFHeader, s.token)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func (s *site) get(path string, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do("GET", path, nil, headers...)
}

func (s *site) post(path string, form url.Values, headers ...[2]string) *httptest.ResponseRecorder {
	s.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return s.do("POST", path, form, headers...)
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int, label string) string {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %.1500s", label, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func contains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("response misses %q:\n%s", want, body)
		}
	}
}

func lacks(t *testing.T, body string, nots ...string) {
	t.Helper()
	for _, not := range nots {
		if strings.Contains(body, not) {
			t.Errorf("response has %q:\n%s", not, body)
		}
	}
}

func goldenFrame(c tpltest.Case) *tpl.Frame {
	f := &tpl.Frame{Data: c.Context}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	if v, ok := c.Context["mode"].(string); ok {
		f.Mode = v
	}
	f.Paused, _ = c.Context["paused"].(bool)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	if m, ok := c.Context["toast"].(map[string]any); ok {
		f.Toast = &tpl.Toast{Category: m["category"].(string), Title: m["title"].(string), Description: m["description"].(string)}
	}
	return f
}

func goldenEngine(t testing.TB, c tpltest.Case) *tpl.Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := tpl.New(tpl.Config{AssetVersion: version, Globals: globals, Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
