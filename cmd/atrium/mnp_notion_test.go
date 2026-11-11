package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
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

func TestNotionSettingsAndExportRemainReachable(t *testing.T) {
	b, _ := mnpNotionSite(t, &mnpNotion{parents: map[string]string{}}, "")
	b.want(http.MethodGet, "/settings/notion", nil, http.StatusOK, "wiki_root", "sensitivity_property", "notion_private_value", "wiki_log_title")
	b.want(http.MethodGet, "/settings/integrations", nil, http.StatusOK, "Export knowledge to Notion", "export_notion_parent")
}

func notionHostConfig(t *testing.T, dir, api, token string) string {
	t.Helper()
	keyFile := filepath.Join(dir, "notion.token")
	if token != "" {
		if err := os.WriteFile(keyFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	anthropicKey := filepath.Join(dir, "anthropic.key")
	if err := os.WriteFile(anthropicKey, []byte("dummy-anthropic-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("[connections.\"service/anthropic\"]\n")
	b.WriteString("kind = \"service/anthropic\"\n")
	b.WriteString("label = \"Anthropic\"\n")
	b.WriteString("capabilities = [\"models\"]\n")
	b.WriteString("credential_files = { api_key = " + fmt.Sprintf("%q", anthropicKey) + " }\n\n")
	b.WriteString("[connections.\"service/notion\"]\n")
	b.WriteString("kind = \"service/notion\"\n")
	b.WriteString("label = \"Notion\"\n")
	b.WriteString("config = { api = " + fmt.Sprintf("%q", api) + " }\n")
	b.WriteString("capabilities = [\"api\"]\n")
	if token != "" {
		b.WriteString("credential_files = { token = " + fmt.Sprintf("%q", keyFile) + " }\n")
	}
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type notionAccess struct {
	mu    sync.Mutex
	pages int
}

func (f *notionAccess) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer ntn_good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"object":"error","status":401,"code":"unauthorized","message":"API token is invalid."}`))
		return
	}
	switch r.URL.Path {
	case "/v1/users/me":
		_, _ = w.Write([]byte(`{"object":"user","id":"bot","type":"bot"}`))
	case "/v1/search":
		results := make([]map[string]string, f.pages)
		for i := range results {
			results[i] = map[string]string{"object": "page", "id": "p"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "results": results, "has_more": false, "next_cursor": nil})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestNotionConnectionTestSavesOnlyASharedToken(t *testing.T) {
	fake := &notionAccess{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", notionHostConfig(t, dir, srv.URL, ""))
	s := &integrationSite{t: t, dir: dir}
	s.start()

	stored := func() bool {
		t.Helper()
		var n int
		if err := mustConnect(t, filepath.Join(dir, "atrium.db")).Read.
			QueryRow("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/notion' AND slot = 'token'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if code, body := s.conn("/test", url.Values{"id": {"service/notion"}, "credential.token": {"ntn_bad"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Notion refused the token") {
		t.Fatalf("a refused token = %d: %.300s", code, body)
	}
	if stored() {
		t.Fatal("a refused Notion token was stored")
	}
	if code, body := s.conn("/test", url.Values{"id": {"service/notion"}, "credential.token": {"ntn_good"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "no pages shared with the integration") {
		t.Fatalf("a token with no shared pages = %d: %.300s", code, body)
	}
	if stored() {
		t.Fatal("a token with no shared pages was stored")
	}
	fake.mu.Lock()
	fake.pages = 3
	fake.mu.Unlock()
	if code, body := s.conn("/test", url.Values{"id": {"service/notion"}, "credential.token": {"ntn_good"}}); code != http.StatusOK {
		t.Fatalf("a shared token = %d: %.300s", code, body)
	}
	if !stored() {
		t.Fatal("a working Notion token was not stored")
	}
	if got := connectionState(t, dir, "service/notion"); got != "ok" {
		t.Fatalf("connection state = %q", got)
	}
	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/settings/connections", nil); code != http.StatusOK || strings.Contains(body, "ntn_good") {
		t.Fatalf("the connections page = %d and rendered the stored token", code)
	}
	tested := runApp(t, "connections", "test", "service/notion")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
}
