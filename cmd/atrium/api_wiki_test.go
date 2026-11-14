package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type wikiNotion struct {
	hold     atomic.Bool
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	markdown string
	edited   string
}

const (
	wikiPublic  = "44444444-4444-4444-4444-444444444444"
	wikiPrivate = "55555555-5555-5555-5555-555555555555"
)

func (f *wikiNotion) page(id, title, sensitivity string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{
		"object": "page", "id": id, "url": "https://notion.example/" + id, "last_edited_time": f.edited, "in_trash": false,
		"parent": map[string]any{"type": "workspace"},
		"properties": map[string]any{
			"title":       map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": title}}},
			"sensitivity": map[string]any{"type": "formula", "formula": map[string]any{"type": "string", "string": sensitivity}},
		},
	}
}

func (f *wikiNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/v1/search":
		var body struct {
			Filter struct {
				Value string `json:"value"`
			} `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		results := []any{}
		if body.Filter.Value == "page" {
			results = append(results, f.page(wikiPublic, "Garden plan", "public"), f.page(wikiPrivate, "Secret page", "private"))
		}
		reply(map[string]any{"object": "list", "results": results, "has_more": false, "next_cursor": nil})
	case "/v1/pages/" + wikiPublic + "/markdown":
		if f.hold.Load() {
			f.once.Do(func() { close(f.entered) })
			<-f.release
		}
		f.mu.Lock()
		md := f.markdown
		f.mu.Unlock()
		reply(map[string]any{"markdown": md, "truncated": false})
	case "/v1/pages/" + wikiPrivate + "/markdown":
		reply(map[string]any{"markdown": "secret compost alpha", "truncated": false})
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"object": "error", "status": 404, "code": "object_not_found", "message": "missing"})
	}
}

func wikiHits(t *testing.T, data map[string]any) []string {
	t.Helper()
	hits, _ := data["hits"].([]any)
	titles := make([]string, 0, len(hits))
	for _, h := range hits {
		titles = append(titles, h.(map[string]any)["title"].(string))
	}
	return titles
}

func TestWikiAPI(t *testing.T) {
	fake := &wikiNotion{entered: make(chan struct{}), release: make(chan struct{}), markdown: "compost alpha", edited: "2026-01-01T00:00:00.000Z"}
	fake.hold.Store(true)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	f := newFixture(t, goldenData{}, nil, "")
	setEnv(t, "ATRIUM_HOST_CONFIG", notionHostConfig(t, f.dir, srv.URL, "ntn_good"))
	token := localAPIToken(t, "api")
	opsAddr(t)
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	first := make(chan apiReply, 1)
	go func() { first <- s.do(http.MethodPost, "/api/v1/wiki/sync", token, nil) }()
	<-fake.entered
	expectAPI(t, s.do(http.MethodPost, "/api/v1/wiki/sync", token, nil), http.StatusConflict, "conflict")
	fake.hold.Store(false)
	close(fake.release)
	if got := apiData(t, <-first, http.StatusOK); number(t, got["new"]) != 2 || number(t, got["private"]) != 1 {
		t.Fatalf("first sync: %v", got)
	}

	search := "/api/v1/wiki?q=compost"
	tag := expectConditional(t, s, token, search)
	if titles := wikiHits(t, apiData(t, s.do(http.MethodGet, search, token, nil), http.StatusOK)); len(titles) != 1 || titles[0] != "Garden plan" {
		t.Fatalf("public search: %v", titles)
	}
	if titles := wikiHits(t, apiData(t, s.do(http.MethodGet, search+"&private=true", token, nil), http.StatusOK)); len(titles) != 2 {
		t.Fatalf("private search: %v", titles)
	}
	page := apiData(t, s.do(http.MethodGet, "/api/v1/wiki/"+wikiPublic, token, nil), http.StatusOK)["page"].(map[string]any)
	if page["markdown"] != "compost alpha" || page["title"] != "Garden plan" {
		t.Fatalf("detail: %v", page)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/wiki/66666666-6666-6666-6666-666666666666", token, nil), http.StatusNotFound, "not_found")
	expectField(t, s.do(http.MethodGet, search+"&limit=0", token, nil), "limit")

	fake.mu.Lock()
	fake.markdown, fake.edited = "compost alpha gamma", "2026-01-02T00:00:00.000Z"
	fake.mu.Unlock()
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/wiki/sync", token, nil), http.StatusOK); number(t, got["updated"]) < 1 {
		t.Fatalf("second sync: %v", got)
	}
	expectChanged(t, s, token, search, tag)
	page = apiData(t, s.do(http.MethodGet, "/api/v1/wiki/"+wikiPublic, token, nil), http.StatusOK)["page"].(map[string]any)
	if !strings.Contains(page["markdown"].(string), "gamma") {
		t.Fatalf("detail after the second sync: %v", page)
	}
}
