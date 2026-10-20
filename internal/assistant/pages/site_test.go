package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const zone = "America/Port_of_Spain"

type site struct {
	t      *testing.T
	db     *db.DB
	cfg    config.Config
	api    *anthropictest.Server
	srv    *web.Server
	http   *httptest.Server
	token  string
	mu     sync.Mutex
	clock  float64
	runner *loop.Runner
}

type siteOption func(*site)

func withRunner(r *loop.Runner) siteOption { return func(s *site) { s.runner = r } }

func newSite(t *testing.T, options ...siteOption) *site {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": zone,
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{t: t, db: d, cfg: cfg, api: anthropictest.New(t), clock: 1_780_000_000, runner: &loop.Runner{Registry: registry.New()}}
	for _, o := range options {
		o(s)
	}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	module := Module(Options{Runner: s.runner, Client: s.api.Client(), Now: s.now, Poll: 10 * time.Millisecond, KeepAlive: 200 * time.Millisecond})
	s.srv, err = web.New(web.Options{Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, Render: render, Modules: []web.Module{module}})
	if err != nil {
		t.Fatal(err)
	}
	s.http = httptest.NewServer(s.srv)
	t.Cleanup(func() {
		s.http.Close()
		s.srv.Close()
	})
	resp := s.do(http.MethodGet, "/chat", nil, "")
	for _, c := range resp.Cookies() {
		if c.Name == web.CSRFCookie {
			s.token = c.Value
		}
	}
	if s.token == "" {
		t.Fatal("no csrf cookie")
	}
	return s
}

func (s *site) now() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock++
	return s.clock
}

func (s *site) setClock(at float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = at
}

type reply struct {
	*http.Response
	body string
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (s *site) request(method, target string, body io.Reader, contentType string, headers ...string) *http.Request {
	s.t.Helper()
	req, err := http.NewRequest(method, s.http.URL+target, body)
	if err != nil {
		s.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if s.token != "" {
		req.AddCookie(&http.Cookie{Name: web.CSRFCookie, Value: s.token})
		req.Header.Set(web.CSRFHeader, s.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return req
}

func (s *site) do(method, target string, body io.Reader, contentType string, headers ...string) *reply {
	s.t.Helper()
	resp, err := noRedirect.Do(s.request(method, target, body, contentType, headers...))
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return &reply{resp, string(b)}
}

// chunked sends the body without a Content-Length. A Go client pushing a 66 MB Content-Length body at this server
// over loopback trickles on the artemis build host (about 100 KB/s, ten minutes for the refusal), while chunked
// transfer, curl and a plain net/http server are all fast; see the over-limit upload test.
func (s *site) chunked(method, target string, body io.Reader, contentType string, headers ...string) *reply {
	s.t.Helper()
	return s.do(method, target, struct{ io.Reader }{body}, contentType, headers...)
}

func (s *site) get(target string, headers ...string) *reply {
	s.t.Helper()
	return s.do(http.MethodGet, target, nil, "", headers...)
}

func (s *site) post(target string, form url.Values, headers ...string) *reply {
	s.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return s.do(http.MethodPost, target, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", headers...)
}

type upload struct {
	name, contentType string
	data              []byte
}

func (s *site) multipart(target string, fields map[string]string, uploads []upload, headers ...string) *reply {
	s.t.Helper()
	return s.multipartVia(s.do, target, fields, uploads, headers...)
}

type doer func(method, target string, body io.Reader, contentType string, headers ...string) *reply

func (s *site) multipartVia(do doer, target string, fields map[string]string, uploads []upload, headers ...string) *reply {
	s.t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for _, u := range uploads {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="upload"; filename="`+strings.ReplaceAll(u.name, `"`, `\"`)+`"`)
		h.Set("Content-Type", u.contentType)
		part, err := w.CreatePart(h)
		if err != nil {
			s.t.Fatal(err)
		}
		_, _ = part.Write(u.data)
	}
	_ = w.Close()
	return do(http.MethodPost, target, &b, w.FormDataContentType(), headers...)
}

func (s *site) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Write.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *site) count(query string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.db.Read.QueryRow(query, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *site) settle(runID int64) *journal.Run {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := journal.GetRun(context.Background(), s.db.Read, runID)
		if err != nil {
			s.t.Fatal(err)
		}
		if r != nil && r.State != journal.Running {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("run %d still running", runID)
	return nil
}

func (s *site) lastRun(threadID int64) int64 {
	s.t.Helper()
	var id int64
	if err := s.db.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM runs WHERE thread_id = ?", threadID).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

const hx = "HX-Request"

func status(t *testing.T, r *reply, want int) string {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", r.StatusCode, want, r.body)
	}
	return r.body
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body misses %q", want)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
}

func detail(t *testing.T, body string) any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	return out["detail"]
}

func reply1(blocks ...json.RawMessage) anthropictest.Response {
	return anthropictest.Reply("end_turn", blocks...)
}
