package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/settings"
)

type notionPagesFake struct {
	mu       sync.Mutex
	pages    map[string]map[string]any
	markdown map[string]string
	edits    int
}

func notionKey(id string) string { return strings.ReplaceAll(id, "-", "") }

func (f *notionPagesFake) add(id, title, parent, markdown string) {
	f.pages[notionKey(id)] = map[string]any{
		"object": "page", "id": id, "url": "https://notion.invalid/" + notionKey(id), "in_trash": false,
		"parent":           map[string]any{"type": "page_id", "page_id": parent},
		"last_edited_time": "2026-01-01T00:00:00.000Z",
		"properties": map[string]any{
			"title": map[string]any{"id": "title", "type": "title", "title": []any{
				map[string]any{"type": "text", "plain_text": title, "text": map[string]any{"content": title}},
			}},
			"sensitivity": map[string]any{"id": "sens", "type": "select", "select": map[string]any{"name": "public"}},
		},
	}
	f.markdown[notionKey(id)] = markdown
}

func (f *notionPagesFake) text(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.markdown[notionKey(id)]
}

func (f *notionPagesFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(status int, payload any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}
	missing := map[string]any{"object": "error", "status": 404, "code": "object_not_found", "message": "missing"}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/pages":
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.pages)+900)
		parent, _ := body["parent"].(map[string]any)
		f.add(id, "Wiki Log", fmt.Sprint(parent["page_id"]), "")
		reply(http.StatusOK, f.pages[notionKey(id)])
	case strings.HasPrefix(r.URL.Path, "/v1/pages/"):
		id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/pages/"), "/")
		key := notionKey(id)
		page, ok := f.pages[key]
		switch {
		case !ok:
			reply(http.StatusNotFound, missing)
		case sub == "markdown" && r.Method == http.MethodPatch:
			update, _ := body["replace_content"].(map[string]any)
			f.markdown[key], _ = update["new_str"].(string)
			f.edits++
			page["last_edited_time"] = fmt.Sprintf("2026-01-01T00:00:%02d.000Z", f.edits)
			reply(http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": f.markdown[key], "truncated": false})
		case sub == "markdown":
			reply(http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": f.markdown[key], "truncated": false})
		default:
			reply(http.StatusOK, page)
		}
	default:
		reply(http.StatusNotFound, missing)
	}
}

func TestNotionPageButtonsWriteDirectlyWithAnUndoEntry(t *testing.T) {
	t.Parallel()
	const (
		root   = "00000000-0000-0000-0000-000000000100"
		plan   = "11111111-1111-1111-1111-111111111111"
		budget = "22222222-2222-2222-2222-222222222222"
		before = "Kitchen plan\n\nOld fact: tiles are white."
	)
	fake := &notionPagesFake{pages: map[string]map[string]any{}, markdown: map[string]string{}}
	fake.add(root, "Wiki Home", root, "")
	fake.add(plan, "Plan", root, before)
	fake.add(budget, "Budget", root, "Money for the kitchen")
	notionSrv := httptest.NewServer(fake)
	t.Cleanup(notionSrv.Close)

	var chatMu sync.Mutex
	var answer string
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatMu.Lock()
		defer chatMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	}))
	t.Cleanup(chatSrv.Close)
	answerWith := func(content string) {
		chatMu.Lock()
		defer chatMu.Unlock()
		answer = content
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_NOTION_BASE_URL", notionSrv.URL)
	setEnv(t, settingEnv(notion.WikiRootKey), root)
	setEnv(t, settingEnv(endpoint.KeyChatURL), chatSrv.URL+"/v1")
	ctx := settings.With(context.Background(), settings.New())
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ id, title, markdown string }{{plan, "Plan", before}, {budget, "Budget", "Money for the kitchen"}} {
		if _, err := seed.Write.Exec("INSERT INTO notion_pages (id, title, url, markdown, parent_type, parent_id, last_edited_time, edited_ts) VALUES (?, ?, ?, ?, 'page_id', ?, '2026-01-01T00:00:00.000Z', 1)",
			p.id, p.title, "https://notion.invalid/"+notionKey(p.id), p.markdown, root); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if status, body, _ := fetch(t, client, http.MethodGet, base+"/notion/"+plan, nil); status != http.StatusOK || !strings.Contains(body, "Edit page") {
		t.Fatalf("GET /notion page = %d: %.400s", status, body)
	}

	answerWith(`{"updates": [{"old_str": "nothing like this", "new_str": "x"}], "summary": "noop"}`)
	if status, body := hxFetch(t, client, http.MethodPost, base+"/notion/"+plan+"/edit", url.Values{"instruction": {"shorten"}}); status != http.StatusOK ||
		!strings.Contains(body, "the model drafted no applicable edit") || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("edit without an applicable draft = %d: %s", status, body)
	}

	answerWith(`{"updates": [{"old_str": "Old fact: tiles are white.", "new_str": "Tiles are grey."}], "summary": "Tiles are now grey"}`)
	status, body := hxFetch(t, client, http.MethodPost, base+"/notion/"+plan+"/edit", url.Values{"instruction": {"tiles changed to grey"}})
	if status != http.StatusOK || !strings.Contains(body, "Edited") || !strings.Contains(body, "Tiles are now grey") || !strings.Contains(body, `href="/activity?category=wiki"`) {
		t.Fatalf("edit = %d: %s", status, body)
	}
	afterEdit := fake.text(plan)
	if !strings.Contains(afterEdit, "Tiles are grey.") || strings.Contains(afterEdit, "Old fact") || !strings.Contains(afterEdit, "Last reviewed") {
		t.Fatalf("Notion page after the edit = %q", afterEdit)
	}

	answerWith(`{"link": true, "reason": "same kitchen project"}`)
	if status, body := hxFetch(t, client, http.MethodPost, base+"/notion/"+plan+"/link", url.Values{"target": {budget}}); status != http.StatusOK ||
		!strings.Contains(body, "Linked") || !strings.Contains(body, "Budget") {
		t.Fatalf("link = %d: %s", status, body)
	}
	if got := fake.text(plan); !strings.Contains(got, "Related") || !strings.Contains(got, "- [Budget](https://notion.invalid/"+notionKey(budget)+")") {
		t.Fatalf("Notion page after the link = %q", got)
	}
	if status, body := hxFetch(t, client, http.MethodPost, base+"/notion/"+plan+"/link", url.Values{"target": {budget}}); status != http.StatusOK ||
		!strings.Contains(body, "the pages are already linked") {
		t.Fatalf("second link = %d: %s", status, body)
	}

	check, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	var proposalsMade, held int
	if err := check.Read.QueryRow("SELECT (SELECT COUNT(*) FROM proposals), (SELECT COUNT(*) FROM agent_writes WHERE state <> 'ok')").Scan(&proposalsMade, &held); err != nil {
		t.Fatal(err)
	}
	if proposalsMade != 0 || held != 0 {
		t.Fatalf("proposals = %d, unsettled writes = %d; want direct writes only", proposalsMade, held)
	}
	rows, err := check.Read.Query("SELECT id FROM agent_writes WHERE tool = 'notion_update_page' AND target = ? AND undo_json IS NOT NULL ORDER BY id", plan)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) != 2 {
		t.Fatalf("undoable page writes = %v, want the edit and the link", ids)
	}

	_, done, _ := fetch(t, client, http.MethodGet, base+"/activity?category=wiki", nil)
	for _, id := range ids {
		if !strings.Contains(done, "/activity/write/"+id+"/undo") {
			t.Fatalf("Done does not offer undo for write %s: %.600s", id, done)
		}
	}
	if status, body := hxFetch(t, client, http.MethodPost, base+"/activity/write/"+ids[1]+"/undo", url.Values{}); status != http.StatusOK ||
		!regexp.MustCompile(`Change \d+ was reverted`).MatchString(body) {
		t.Fatalf("undo of the link = %d: %.600s", status, body)
	}
	if got := fake.text(plan); got != afterEdit {
		t.Fatalf("Notion page after undoing the link = %q, want %q", got, afterEdit)
	}
}
