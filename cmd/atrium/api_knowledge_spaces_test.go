package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func spaceItem(t *testing.T, items []any, key string, want any) map[string]any {
	t.Helper()
	for _, item := range items {
		m := item.(map[string]any)
		if m[key] == want {
			return m
		}
	}
	return nil
}

func TestKnowledgeSpacesKeepProjectContent(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	instructions := "Answer in short paragraphs.\n\n## Brief\n\nRebuild the ferry booking site.\n\n- Keep fares visible"
	setup := []string{
		"INSERT INTO projects (id, name, instructions, effort, context_limit, wiki_hub_id, private, created_at, updated_at) VALUES " +
			"(1, 'Harbor rebuild', '" + instructions + "', 'high', 200000, 'hub-legacy', 1, 0, 0), (2, 'Studio', '', 'low', 100000, NULL, 0, 0, 0)",
		"INSERT INTO threads (id, kind, title, effort, created_at, updated_at, project_id) VALUES " +
			"(2, 'chat', 'Harbor chat', 'high', 0, 5, 1), (3, 'chat', 'Studio chat', 'low', 0, 0, 2)",
		"INSERT INTO tasks (title, notes, status, priority, source_kind, source_id, created_ts, updated_ts) VALUES " +
			"('Send the fare table', '', 'open', 'normal', 'project', '1', 0, 0), ('Studio errand', '', 'open', 'normal', 'project', '2', 0, 0)",
	}
	for _, stmt := range setup {
		if _, err := d.Write.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	before := len(api.Requests())
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
	if ran := runApp(t, "agent", "run", "--thread", "2", "--text", "hello"); ran.code != cli.ExitOK {
		t.Fatalf("space thread run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()
	if len(requests) != before+1 {
		t.Fatalf("space thread made %d model requests, want 1", len(requests)-before)
	}
	if prompt := systemText(t, requests[before]); !strings.Contains(prompt, "<project_instructions>\n"+instructions+"\n</project_instructions>") {
		t.Fatalf("the space thread prompt lacks its instructions: %.800s", prompt)
	}

	token := localAPIToken(t, "spaces")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}

	for _, old := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects"}, {http.MethodGet, "/api/v1/projects/1"}, {http.MethodPost, "/api/v1/projects"},
		{http.MethodGet, "/api/v1/projects/1/files"},
	} {
		if got := s.do(old.method, old.path, token, map[string]any{"name": "Old"}); got.status != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", old.method, old.path, got.status)
		}
	}

	list := s.do(http.MethodGet, "/api/v1/knowledge/spaces", token, nil)
	harbor := spaceItem(t, apiReplyItems(t, list), "name", "Harbor rebuild")
	if harbor == nil || harbor["private"] != true || harbor["instructions"] != instructions || harbor["thread_count"] != float64(1) || harbor["wiki_hub_id"] != nil {
		t.Fatalf("space list: %s", list.body)
	}

	body, media := uploadBody(t, nil, "fares.txt", "Adult fare 12, child fare 6.")
	uploaded := s.do(http.MethodPost, "/api/v1/knowledge/spaces/1/files", token, body, "Content-Type", media)
	if uploaded.status != http.StatusCreated {
		t.Fatalf("upload: %d %s", uploaded.status, uploaded.body)
	}
	file := number(t, uploaded.json["file_ids"].([]any)[0])
	expectAPI(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/knowledge/spaces/2/files/%d", file), token, nil), http.StatusNotFound, "not_found")
	if got := s.do(http.MethodGet, fmt.Sprintf("/api/v1/knowledge/spaces/1/files/%d/content", file), token, nil); got.status != http.StatusOK || got.body != "Adult fare 12, child fare 6." {
		t.Fatalf("space file content: %d %s", got.status, got.body)
	}

	record := number(t, apiData(t, s.do(http.MethodPost, "/api/v1/knowledge", token, map[string]any{"title": "Fare policy", "body": "Children ride at half fare.", "scope": "work"}), http.StatusCreated)["id"])
	linksPath := fmt.Sprintf("/api/v1/knowledge/%d/links", record)
	expectAPI(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "space", "target_id": "999", "relation": "part_of"}), http.StatusConflict, "refused")
	link := apiData(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "space", "target_id": "1", "relation": "part_of"}), http.StatusCreated)
	if target := link["target"].(map[string]any); target["title"] != "Harbor rebuild" || target["state"] != "live" || target["address"] != "/knowledge/spaces/1" {
		t.Fatalf("space link: %v", link)
	}

	pagePath := "/api/v1/knowledge/spaces/1"
	page := s.do(http.MethodGet, pagePath, token, nil)
	if page.status != http.StatusOK {
		t.Fatalf("space page: %d %s", page.status, page.body)
	}
	space := page.json["space"].(map[string]any)
	if space["name"] != "Harbor rebuild" || space["instructions"] != instructions || space["private"] != true || space["archived"] != false {
		t.Fatalf("space fields: %s", page.body)
	}
	if brief := spaceItem(t, page.json["sections"].([]any), "heading", "Brief"); brief == nil || len(brief["blocks"].([]any)) != 2 {
		t.Fatalf("space sections: %v", page.json["sections"])
	}
	if f := spaceItem(t, page.json["files"].([]any), "name", "fares.txt"); f == nil || f["space_id"] != float64(1) {
		t.Fatalf("space files: %v", page.json["files"])
	}
	threads := page.json["threads"].([]any)
	if len(threads) != 1 || spaceItem(t, threads, "title", "Harbor chat") == nil {
		t.Fatalf("space threads: %v", threads)
	}
	tasks := page.json["tasks"].([]any)
	if len(tasks) != 1 || spaceItem(t, tasks, "title", "Send the fare table") == nil {
		t.Fatalf("space tasks: %v", tasks)
	}
	records := page.json["records"].([]any)
	if r := spaceItem(t, records, "title", "Fare policy"); len(records) != 1 || r == nil || r["relation"] != "part_of" || r["state"] != "live" || r["address"] != fmt.Sprintf("/knowledge/%d", record) {
		t.Fatalf("space records: %v", records)
	}
	if counts := page.json["counts"].(map[string]any); counts["files"] != float64(1) || counts["threads"] != float64(1) || counts["tasks"] != float64(1) || counts["records"] != float64(1) {
		t.Fatalf("space counts: %v", counts)
	}
	related := apiData(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/knowledge/%d/page", record), token, nil), http.StatusOK)
	if back := linkRelated(t, related, "space", "1"); back == nil || back["address"] != "/knowledge/spaces/1" || back["title"] != "Harbor rebuild" {
		t.Fatalf("record page related: %v", related["related"])
	}

	tag := page.header.Get("ETag")
	if patched := s.do(http.MethodPatch, pagePath, token, map[string]any{"private": false}); patched.status != http.StatusOK {
		t.Fatalf("patch: %d %s", patched.status, patched.body)
	}
	after := s.do(http.MethodGet, pagePath, token, nil, "If-None-Match", tag)
	if after.status != http.StatusOK || after.json["space"].(map[string]any)["private"] != false || after.json["space"].(map[string]any)["instructions"] != instructions {
		t.Fatalf("patched space: %d %s", after.status, after.body)
	}
	if archived := s.do(http.MethodPost, "/api/v1/knowledge/spaces/2/archive", token, map[string]any{"archived": true}); archived.status != http.StatusOK {
		t.Fatalf("archive: %d %s", archived.status, archived.body)
	}
	studio := apiData(t, s.do(http.MethodPost, linksPath, token, map[string]any{"target_kind": "space", "target_id": "2", "relation": "see_also"}), http.StatusCreated)
	if target := studio["target"].(map[string]any); target["state"] != "archived" {
		t.Fatalf("archived space link: %v", studio)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/knowledge/spaces/2/files", token, body, "Content-Type", media), http.StatusConflict, "project_archived")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads WHERE project_id = 2 AND deleted_at IS NULL"); n != 1 {
		t.Fatalf("archiving lost the studio thread: %d", n)
	}
}
