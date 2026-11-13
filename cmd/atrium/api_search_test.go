package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func searchIDs(t *testing.T, items []any, nested bool) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		row := item.(map[string]any)
		if nested {
			if row["type"] != "thread" {
				t.Fatalf("search result is not a thread: %v", row)
			}
			row = row["thread"].(map[string]any)
		}
		ids = append(ids, number(t, row["id"]))
	}
	return ids
}

func TestSearchAPI(t *testing.T) {
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, model)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	asstExec(t, d, "INSERT INTO projects (id, name, effort, context_limit, private, created_at, updated_at) VALUES (901, 'hidden', 'low', 100000, 1, 0, 0)")
	for i := range 105 {
		id := 5000 + i
		asstExec(t, d, "INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (?, 'chat', ?, 'low', ?, ?)", id, fmt.Sprintf("kestrel %d", i), i, i)
		asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", id, fmt.Sprintf("kestrel note %d", i))
	}
	asstExec(t, d, "INSERT INTO threads (id, kind, title, effort, created_at, updated_at, project_id) VALUES (6000, 'chat', 'kestrel private', 'low', 200, 200, 901)")
	asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (6000, 'kestrel private plan')")
	asstExec(t, d, "INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (6001, 'chat', 'heron', 'low', 300, 300)")
	asstExec(t, d, "INSERT INTO thread_fts (thread_id, text) VALUES (6001, 'heron watch')")
	asstExec(t, d, "INSERT INTO tasks (title, due, status, created_ts, updated_ts) VALUES ('Renew passport', '2000-01-01', 'open', 1, 1)")
	token := localAPIToken(t, "search-api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}

	first := s.do(http.MethodGet, "/api/v1/search?q=kestrel&limit=100", token, nil)
	page := searchIDs(t, apiItems(t, first), true)
	cursor, _ := first.json["next_cursor"].(string)
	if len(page) != 100 || cursor == "" {
		t.Fatalf("first search page: %d %q", len(page), cursor)
	}
	rest := s.do(http.MethodGet, "/api/v1/search?q=kestrel&limit=100&cursor="+cursor, token, nil)
	all := append(page, searchIDs(t, apiItems(t, rest), true)...)
	listed := s.do(http.MethodGet, "/api/v1/threads?q=kestrel&limit=100", token, nil)
	listedRest := s.do(http.MethodGet, "/api/v1/threads?q=kestrel&limit=100&cursor="+listed.json["next_cursor"].(string), token, nil)
	threads := append(searchIDs(t, apiItems(t, listed), false), searchIDs(t, apiItems(t, listedRest), false)...)
	if len(all) != 106 || fmt.Sprint(all) != fmt.Sprint(threads) {
		t.Fatalf("search differs from the thread list:\n%v\n%v", all, threads)
	}
	if all[0] != 6000 {
		t.Fatalf("search order: first %d, want the newest thread 6000", all[0])
	}
	for _, id := range all {
		if id == 6001 {
			t.Fatal("search returned a thread that does not match")
		}
	}
	scoped := s.do(http.MethodGet, "/api/v1/search?q=kestrel&project_id=901", token, nil)
	if got := searchIDs(t, apiItems(t, scoped), true); len(got) != 1 || got[0] != 6000 {
		t.Fatalf("project narrowing: %v", got)
	}
	if none := s.do(http.MethodGet, "/api/v1/search?q=heron&project_id=901", token, nil); len(apiItems(t, none)) != 0 {
		t.Fatalf("project narrowing widened: %s", none.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search?q=heron&limit=100&cursor="+cursor, token, nil), 400, "bad_cursor")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/threads?q=kestrel&limit=100&cursor="+cursor, token, nil), 400, "bad_cursor")
	empty := s.do(http.MethodGet, "/api/v1/search?q=%20", token, nil)
	expectAPI(t, empty, 422, "invalid")
	if empty.json["error"].(map[string]any)["fields"] == nil {
		t.Fatalf("missing fields: %s", empty.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search?q="+strings.Repeat("k", 201), token, nil), 422, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/search?q=kestrel&limit=101", token, nil), 422, "invalid")
	if punct := s.do(http.MethodGet, "/api/v1/search?q=%22%22", token, nil); punct.status != 200 || len(apiItems(t, punct)) != 0 {
		t.Fatalf("query without terms: %d %s", punct.status, punct.body)
	}
	etag := first.header.Get("ETag")
	if again := s.do(http.MethodGet, "/api/v1/search?q=kestrel&limit=100", token, nil, "If-None-Match", etag); etag == "" || again.status != 304 {
		t.Fatalf("search conditional: %q %d", etag, again.status)
	}
	if archived := s.do(http.MethodPut, "/api/v1/threads/5000/archive", token, map[string]any{"archived": true}); archived.status != 200 {
		t.Fatalf("archive: %d %s", archived.status, archived.body)
	}
	changed := s.do(http.MethodGet, "/api/v1/search?q=kestrel&limit=100", token, nil, "If-None-Match", etag)
	if changed.status != 200 || changed.header.Get("ETag") == etag {
		t.Fatalf("search did not change after archiving: %d", changed.status)
	}
	if archivedHits := s.do(http.MethodGet, "/api/v1/search?q=kestrel&view=archived", token, nil); fmt.Sprint(searchIDs(t, apiItems(t, archivedHits), true)) != "[5000]" {
		t.Fatalf("archived search: %s", archivedHits.body)
	}

	attention := s.do(http.MethodGet, "/api/v1/attention", token, nil)
	if attention.status != 200 || !strings.Contains(attention.body, "Renew passport") {
		t.Fatalf("attention: %d %s", attention.status, attention.body)
	}
	for _, item := range attention.json["items"].([]any) {
		row := item.(map[string]any)
		if row["kind"] == "" || row["source_id"] == "" {
			t.Fatalf("attention item without a reference: %v", row)
		}
		for _, action := range row["actions"].([]any) {
			if _, ok := action.(string); !ok {
				t.Fatalf("attention action is not a bare kind: %v", action)
			}
		}
	}
	for _, leak := range []string{"token", "/actions/", "http"} {
		if strings.Contains(attention.body, leak) {
			t.Fatalf("attention exposes %q: %s", leak, attention.body)
		}
	}
	atag := attention.header.Get("ETag")
	if again := s.do(http.MethodGet, "/api/v1/attention", token, nil, "If-None-Match", atag); atag == "" || again.status != 304 {
		t.Fatalf("attention conditional: %q %d", atag, again.status)
	}
}
