package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
)

type mnpNotion struct {
	mu      sync.Mutex
	refuse  bool
	root    string
	parents map[string]string
	titles  []string
	paths   []string
}

func (f *mnpNotion) titled(title string) (parents []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.titles {
		if t == title {
			parents = append(parents, f.parents[fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)])
		}
	}
	return parents
}

func mnpNotionPage(id, parent, title string) map[string]any {
	return map[string]any{
		"object": "page", "id": id, "url": "https://notion.invalid/" + strings.ReplaceAll(id, "-", ""), "in_trash": false,
		"parent":           map[string]any{"type": "page_id", "page_id": parent},
		"last_edited_time": "2026-01-01T00:00:00.000Z",
		"properties": map[string]any{"title": map[string]any{"id": "title", "type": "title", "title": []any{
			map[string]any{"type": "text", "plain_text": title, "text": map[string]any{"content": title}},
		}}},
	}
}

func (f *mnpNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	reply := func(status int, payload any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}
	missing := map[string]any{"object": "error", "status": 404, "code": "object_not_found", "message": "missing"}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/pages" {
		if f.refuse {
			reply(http.StatusBadRequest, map[string]any{"object": "error", "status": 400, "code": "validation_error", "message": "Notion refused the page"})
			return
		}
		var body struct {
			Parent struct {
				PageID string `json:"page_id"`
			} `json:"parent"`
			Properties struct {
				Title struct {
					Title []struct {
						Text struct {
							Content string `json:"content"`
						} `json:"text"`
					} `json:"title"`
				} `json:"title"`
			} `json:"properties"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		title := ""
		if len(body.Properties.Title.Title) > 0 {
			title = body.Properties.Title.Title[0].Text.Content
		}
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.parents)+1)
		f.parents[id] = body.Parent.PageID
		f.titles = append(f.titles, title)
		reply(http.StatusOK, mnpNotionPage(id, body.Parent.PageID, title))
		return
	}
	id, sub, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/pages/"), "/")
	var parent string
	known := false
	for created, p := range f.parents {
		if strings.ReplaceAll(created, "-", "") == strings.ReplaceAll(id, "-", "") {
			parent, known = p, true
		}
	}
	if f.root != "" && strings.ReplaceAll(id, "-", "") == strings.ReplaceAll(f.root, "-", "") {
		parent, known = f.root, true
	}
	switch {
	case !strings.HasPrefix(r.URL.Path, "/v1/pages/") || !known:
		reply(http.StatusNotFound, missing)
	case sub == "markdown":
		reply(http.StatusOK, map[string]any{"object": "page_markdown", "id": id, "markdown": "", "truncated": false})
	default:
		reply(http.StatusOK, mnpNotionPage(id, parent, "Kitchen"))
	}
}

func mnpNotionSite(t *testing.T, fake *mnpNotion, wikiRoot string) (*mnpBrowser, *db.DB) {
	t.Helper()
	fake.root = wikiRoot
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_NOTION_BASE_URL", srv.URL)
	setEnv(t, settingEnv(notion.WikiRootKey), wikiRoot)
	dir := t.TempDir()
	b := mnpNewBrowser(t, startServeIn(t, dir))
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return b, d
}

func mnpNewProject(t *testing.T, b *mnpBrowser) string {
	t.Helper()
	b.want(http.MethodGet, "/library/wiki", nil, http.StatusOK)
	req, err := http.NewRequest(http.MethodPost, b.base+"/projects", strings.NewReader(url.Values{
		"name": {"Kitchen"}, "instructions": {"Always answer in metric units."}, "effort": {"high"}, "context_limit": {"200000"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range b.client.Jar.Cookies(req.URL) {
		if c.Name == "atrium_csrf" {
			req.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	return strings.TrimSuffix(strings.TrimPrefix(resp.Header.Get("Location"), "/projects/"), "/edit")
}

func mnpHub(t *testing.T, d *db.DB, id string) *string {
	t.Helper()
	var hub *string
	if err := d.Read.QueryRow("SELECT wiki_hub_id FROM projects WHERE id = ?", id).Scan(&hub); err != nil {
		t.Fatal(err)
	}
	return hub
}

func mnpWrites(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM agent_writes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMnpNotionPageMarkdownIsSanitized(t *testing.T) {
	t.Parallel()
	const page = "11111111-1111-1111-1111-111111111111"
	b, d := mnpNotionSite(t, &mnpNotion{parents: map[string]string{}}, "")
	markdown := "# Plan\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1)) [ok](https://example.test/x) [rel](/relative)\n\n<img src=x onerror=alert(2)>\n"
	if _, err := d.Write.Exec("INSERT INTO notion_pages (id, title, url, markdown, private, last_edited_time, edited_ts) VALUES (?, 'Plan', ?, ?, 0, '2026-01-01T00:00:00Z', 1.0)",
		page, "https://www.notion.so/"+page, markdown); err != nil {
		t.Fatal(err)
	}
	body := b.want(http.MethodGet, "/notion/"+page, nil, http.StatusOK, "&lt;script&gt;", `href="https://example.test/x"`, `rel="noopener noreferrer"`)
	for _, bad := range []string{"<script>alert", "<img src=x", `href="javascript:`, `href="/relative"`} {
		if strings.Contains(body, bad) {
			t.Errorf("rendered page contains %q", bad)
		}
	}
}

func TestMnpWikiHubCreateWritesOnceAndRefusalsWriteNothing(t *testing.T) {
	t.Parallel()
	const root = "00000000-0000-0000-0000-000000000100"
	hx := "HX-Request"

	t.Run("created", func(t *testing.T) {
		fake := &mnpNotion{parents: map[string]string{}}
		b, d := mnpNotionSite(t, fake, root)
		id := mnpNewProject(t, b)
		code, reply := b.do(http.MethodPost, "/projects/"+id+"/wiki-hub", url.Values{}, hx, "true")
		if code != http.StatusOK || !strings.Contains(reply, "Wiki hub created") {
			t.Fatalf("create hub = %d, Notion saw %v: %.600s", code, fake.paths, reply)
		}
		hub := mnpHub(t, d, id)
		if kitchens := fake.titled("Kitchen"); hub == nil || len(kitchens) != 1 || kitchens[0] != root {
			t.Fatalf("hub %v, Kitchen page parents %v; want one page under the wiki root", hub, kitchens)
		}
		b.want(http.MethodGet, "/projects/"+id+"/edit", nil, http.StatusOK, *hub)
		var threadID, runID *int64
		if err := d.Read.QueryRow("SELECT thread_id, run_id FROM agent_writes WHERE tool = 'notion_create_page'").Scan(&threadID, &runID); err != nil {
			t.Fatal(err)
		}
		if threadID != nil || runID != nil {
			t.Errorf("hub write belongs to thread %v run %v, want neither", threadID, runID)
		}
		code, again := b.do(http.MethodPost, "/projects/"+id+"/wiki-hub", url.Values{}, hx, "true")
		if code != http.StatusOK || !strings.Contains(again, *hub) || strings.Contains(again, "Wiki hub created") {
			t.Errorf("second create = %d: %.600s", code, again)
		}
		if n := len(fake.titled("Kitchen")); n != 1 || mnpWrites(t, d) != 1 {
			t.Errorf("%d Kitchen pages, %d writes after a repeated create; want 1 and 1", n, mnpWrites(t, d))
		}
	})

	t.Run("missing wiki root", func(t *testing.T) {
		b, d := mnpNotionSite(t, &mnpNotion{parents: map[string]string{}}, "")
		id := mnpNewProject(t, b)
		code, reply := b.do(http.MethodPost, "/projects/"+id+"/wiki-hub", url.Values{}, hx, "true")
		if code != http.StatusUnprocessableEntity || !strings.Contains(reply, "ATRIUM_NOTION_WIKI_ROOT") {
			t.Errorf("create without a wiki root = %d: %.600s", code, reply)
		}
		if hub := mnpHub(t, d, id); hub != nil || mnpWrites(t, d) != 0 {
			t.Errorf("hub %v, %d writes; want none", hub, mnpWrites(t, d))
		}
	})

	t.Run("notion refuses", func(t *testing.T) {
		b, d := mnpNotionSite(t, &mnpNotion{refuse: true, parents: map[string]string{}}, root)
		id := mnpNewProject(t, b)
		code, reply := b.do(http.MethodPost, "/projects/"+id+"/wiki-hub", url.Values{}, hx, "true")
		if code != http.StatusUnprocessableEntity || !strings.Contains(reply, "Notion refused the page") {
			t.Errorf("refused create = %d: %.600s", code, reply)
		}
		if hub := mnpHub(t, d, id); hub != nil || mnpWrites(t, d) != 0 {
			t.Errorf("hub %v, %d writes; want none", hub, mnpWrites(t, d))
		}
	})
}
