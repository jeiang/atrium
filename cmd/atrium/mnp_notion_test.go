package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
