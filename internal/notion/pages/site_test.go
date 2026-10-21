package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/loop"
	assistantpages "github.com/jeiang/atrium/internal/assistant/pages"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/registry"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	hx          = "HX-Request"
	pageID      = "11111111-1111-1111-1111-111111111111"
	secretID    = "22222222-2222-2222-2222-222222222222"
	wikiRootID  = "00000000-0000-0000-0000-000000000100"
	notionToken = "tok"
)

type site struct {
	t     *testing.T
	db    *db.DB
	cfg   config.Config
	srv   *web.Server
	token string
	mu    sync.Mutex
	clock float64
	chat  func(system, user string) (json.RawMessage, error)
}

func newSite(t *testing.T, extra map[string]string) *site {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	vars := map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow",
		"ATRIUM_TZ": "America/Port_of_Spain",
	}
	for k, v := range extra {
		vars[k] = v
	}
	env := config.Map(vars)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := config.LoadPins(env, settings.Default)
	if err != nil {
		t.Fatal(err)
	}
	settings.Default.SetPins(pins)
	t.Cleanup(func() { settings.Default.SetPins(nil) })
	s := &site{t: t, db: d, cfg: cfg, clock: 1_780_000_000}
	render, err := web.NewRenderer(cfg, d, tpl.Config{Modules: []fs.FS{Templates(), assistantpages.Templates()}})
	if err != nil {
		t.Fatal(err)
	}
	chat := func(_ context.Context, _, system, user string, _ any, _ int) (json.RawMessage, error) {
		return s.chat(system, user)
	}
	assistant := assistantpages.Module(assistantpages.Options{Runner: &loop.Runner{Registry: registry.New()}, Now: s.now})
	s.srv, err = web.New(web.Options{
		Cfg: cfg, Env: env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{"testserver"}, Render: render,
		Modules: []web.Module{Module(Options{Chat: chat, Now: s.now}), assistant},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.srv.Close)
	for _, c := range s.get("/notion/none").Result().Cookies() {
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

func (s *site) do(method, target string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = "testserver"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method != http.MethodGet && s.token != "" {
		req.AddCookie(&http.Cookie{Name: web.CSRFCookie, Value: s.token})
		req.Header.Set(web.CSRFHeader, s.token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

func (s *site) get(target string, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(http.MethodGet, target, nil, headers...)
}

func (s *site) post(target string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	return s.do(http.MethodPost, target, form, headers...)
}

func (s *site) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.db.Write.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func (s *site) addPage(id, title, markdown string, private int) {
	s.t.Helper()
	s.exec("INSERT INTO notion_pages (id, title, url, markdown, private, last_edited_time, edited_ts) "+
		"VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', 1.0)", id, title, "https://www.notion.so/"+id, markdown, private)
}

func (s *site) addVector(rid int64, vector []float64) {
	s.t.Helper()
	s.exec("INSERT INTO notion_vec (rowid, visibility, embedding) VALUES (?, 'public', ?)", rid, embed.Serialize(vector))
}

func unit(index, other int, weight float64) []float64 {
	vector := make([]float64, config.EmbedDim)
	vector[index] = 1
	vector[other] = weight
	return vector
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) string {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body)
	}
	return rec.Body.String()
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

func form(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Add(pairs[i], pairs[i+1])
	}
	return v
}

func (s *site) newProject(instructions string) string {
	s.t.Helper()
	rec := s.post("/projects", form("name", "Kitchen", "instructions", instructions, "effort", "high", "context_limit", "200000"))
	if rec.Code != http.StatusSeeOther {
		s.t.Fatalf("create project: status %d: %s", rec.Code, rec.Body)
	}
	return strings.TrimPrefix(rec.Header().Get("Location"), "/projects/")
}

func (s *site) projectOf(id string) *projects.Project {
	s.t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		s.t.Fatal(err)
	}
	found, err := projects.Get(s.t.Context(), s.db.Read, n)
	if err != nil || found == nil {
		s.t.Fatalf("project %s = %v, %v", id, found, err)
	}
	return found
}

func (s *site) hubOf(id string) *string { return s.projectOf(id).WikiHubID }

type fakeNotion struct {
	mu       sync.Mutex
	pages    map[string]map[string]any
	titles   map[string]string
	parents  map[string]string
	markdown map[string]string
	refuse   bool
}

func newFakeNotion(t *testing.T) *fakeNotion {
	t.Helper()
	f := &fakeNotion{pages: map[string]map[string]any{}, titles: map[string]string{}, parents: map[string]string{}, markdown: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	previous := notiontools.NewClient
	notiontools.NewClient = func(token string) *notion.Client {
		c := notion.NewClient(token)
		c.BaseURL = srv.URL
		c.Sleep = func(context.Context, float64) error { return nil }
		return c
	}
	t.Cleanup(func() { notiontools.NewClient = previous })
	return f
}

func (f *fakeNotion) reply(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	missing := map[string]any{"code": "object_not_found", "message": "missing"}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/pages":
		if f.refuse {
			f.reply(w, http.StatusBadRequest, map[string]any{"code": "validation_error", "message": "Notion refused the page"})
			return
		}
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.pages)+1)
		parent, _ := body["parent"].(map[string]any)
		title, _ := body["properties"].(map[string]any)["title"].(map[string]any)["title"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"].(string)
		f.titles[id], f.parents[id] = title, fmt.Sprint(parent["page_id"])
		f.pages[id] = map[string]any{
			"object": "page", "id": id, "url": "https://notion.invalid/" + id, "in_trash": false,
			"parent":           map[string]any{"type": "page_id", "page_id": parent["page_id"]},
			"last_edited_time": "2026-01-01T00:00:00.000Z",
			"properties": map[string]any{"title": map[string]any{"id": "title", "type": "title", "title": []any{
				map[string]any{"type": "text", "plain_text": title, "text": map[string]any{"content": title}},
			}}},
		}
		f.markdown[id], _ = body["markdown"].(string)
		f.reply(w, http.StatusOK, f.pages[id])
	case strings.HasPrefix(r.URL.Path, "/v1/pages/"):
		id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/pages/"), "/")
		page, ok := f.pages[id]
		switch {
		case !ok:
			f.reply(w, http.StatusNotFound, missing)
		case sub == "markdown" && r.Method == http.MethodPatch:
			update, _ := body["replace_content"].(map[string]any)
			f.markdown[id], _ = update["new_str"].(string)
			f.reply(w, http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": f.markdown[id], "truncated": false})
		case sub == "markdown":
			f.reply(w, http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": f.markdown[id], "truncated": false})
		default:
			f.reply(w, http.StatusOK, page)
		}
	default:
		f.reply(w, http.StatusNotFound, missing)
	}
}

func (f *fakeNotion) idsTitled(title string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, t := range f.titles {
		if t == title {
			out = append(out, id)
		}
	}
	return out
}

func (f *fakeNotion) parentOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parents[id]
}
