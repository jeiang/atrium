package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

type fakeWiki struct {
	mu       sync.Mutex
	markdown string
	edited   string
}

func (f *fakeWiki) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/markdown"):
		_ = json.NewEncoder(w).Encode(map[string]any{"markdown": f.markdown, "truncated": false})
	case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/markdown"):
		var body struct {
			Replace struct {
				New string `json:"new_str"`
			} `json:"replace_content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.markdown = body.Replace.New
		f.edited = fmt.Sprintf("t%d", time.Now().UnixNano())
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "log1", "last_edited_time": f.edited, "in_trash": false, "properties": map[string]any{}})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func TestNightlyObservationsBecomeWikiAndProfileProposals(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	wiki := &fakeWiki{edited: "t0"}
	srv := httptest.NewServer(http.HandlerFunc(wiki.handler))
	defer srv.Close()
	setEnv(t, "ATRIUM_NOTION_NOTION_TOKEN", "token")
	setEnv(t, "ATRIUM_NOTION_BASE_URL", srv.URL)
	setEnv(t, "ATRIUM_NOTION_WIKI_ROOT", "0123456789abcdef0123456789abcdef")
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO notion_state (key, value) VALUES ('wiki_log_page', 'log1')"); err != nil {
		t.Fatal(err)
	}
	run := func(subject, value string) {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("o1", "nightly_observe", map[string]any{"subject": subject, "value": value})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		if ran := runApp("agent", "run", "--kind", "nightly", "--text", "nightly"); ran.code != cli.ExitOK {
			t.Fatalf("nightly run: %d %s", ran.code, ran.stderr)
		}
	}
	pending := func(kind string) int64 {
		t.Helper()
		var id int64
		if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = ? AND status = 'pending' ORDER BY id DESC LIMIT 1", kind).Scan(&id); err != nil {
			t.Fatalf("no pending %s proposal: %v", kind, err)
		}
		return id
	}
	run("tea", "green")
	capture := pending("notion_capture")
	if ran := runApp("proposals", "approve", fmt.Sprint(capture)); ran.code != cli.ExitOK {
		t.Fatalf("approve capture: %d %s", ran.code, ran.stderr)
	}
	wiki.mu.Lock()
	body := wiki.markdown
	wiki.mu.Unlock()
	if !strings.Contains(body, "sometimes uses green") {
		t.Fatalf("wiki log = %q", body)
	}
	if ran := runApp("proposals", "undo", fmt.Sprint(capture)); ran.code != cli.ExitOK {
		t.Fatalf("undo capture: %d %s", ran.code, ran.stderr)
	}
	wiki.mu.Lock()
	body = wiki.markdown
	wiki.mu.Unlock()
	if body != "" {
		t.Fatalf("undo did not restore the wiki log: %q", body)
	}
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO agent_profile (key, value, updated_at) VALUES ('tea', 'black', 0)"); err != nil {
		t.Fatal(err)
	}
	run("tea", "green")
	update := pending("profile_update")
	if ran := runApp("proposals", "approve", fmt.Sprint(update)); ran.code != cli.ExitOK {
		t.Fatalf("approve profile update: %d %s", ran.code, ran.stderr)
	}
	var value string
	if err := d.Read.QueryRow("SELECT value FROM agent_profile WHERE key = 'tea'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "green" {
		t.Fatalf("profile = %q", value)
	}
	old := float64(time.Now().Unix()) - 15*86400
	if _, err := d.Write.Exec("INSERT INTO nightly_observations (subject, value, first_seen, last_seen, count) VALUES ('coffee', 'black', ?, ?, 1)", old, old); err != nil {
		t.Fatal(err)
	}
	run("coffee", "green")
	var note string
	if err := d.Read.QueryRow("SELECT payload FROM proposals WHERE kind = 'notion_capture' AND status = 'pending' ORDER BY id DESC LIMIT 1").Scan(&note); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "now uses green (was black)") {
		t.Fatalf("replace proposal payload = %q", note)
	}
}

func TestStaleWikiToolUsesTheThreshold(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	old := time.Now().AddDate(0, -7, 0).Format("2006-01-02")
	recent := time.Now().AddDate(0, -5, 0).Format("2006-01-02")
	if _, err := d.Write.Exec("INSERT INTO wiki_pages (page_id, title, last_reviewed) VALUES ('p1', 'Old page', ?), ('p2', 'Recent page', ?)", old, recent); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "stale_wiki", map[string]any{})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		if ran := runApp("agent", "run", "--kind", "cron", "--text", "stale"); ran.code != cli.ExitOK {
			t.Fatalf("stale run: %d %s", ran.code, ran.stderr)
		}
		var threadID int64
		if err := d.Read.QueryRow("SELECT MAX(id) FROM threads").Scan(&threadID); err != nil {
			t.Fatal(err)
		}
		rows, err := d.Read.Query("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' ORDER BY seq", threadID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var all strings.Builder
		for rows.Next() {
			var content string
			if err := rows.Scan(&content); err != nil {
				t.Fatal(err)
			}
			all.WriteString(content)
		}
		return all.String()
	}
	body := run()
	if !strings.Contains(body, "Old page") || strings.Contains(body, "Recent page") {
		t.Fatalf("stale list at 6 months = %s", body)
	}
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('nightly_stale_months', '3')"); err != nil {
		t.Fatal(err)
	}
	body = run()
	if !strings.Contains(body, "Old page") || !strings.Contains(body, "Recent page") {
		t.Fatalf("stale list at 3 months = %s", body)
	}
}
