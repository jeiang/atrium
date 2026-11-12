package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestPermissionsPageSavesWithinBounds(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	status, page, _ := fetch(t, client, http.MethodGet, base+"/settings/permissions", nil)
	if status != http.StatusOK || !strings.Contains(page, "<title>Assistant · Settings") {
		t.Fatalf("GET /settings/permissions = %d, on the Assistant page %v", status, strings.Contains(page, "<title>Assistant · Settings"))
	}
	if n := strings.Count(page, `name="permission_`); n != 24 {
		t.Fatalf("%d permission selects, want 24", n)
	}
	noFollow := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, body, header := fetch(t, noFollow, http.MethodPost, base+"/settings/permissions", url.Values{"permission_internet_kinds": {"allow"}})
	if status != http.StatusSeeOther || header.Get("Location") != "/settings/assistant?saved=permissions" {
		t.Fatalf("save = %d %q: %s", status, header.Get("Location"), body)
	}
	if status, activity, _ := fetch(t, client, http.MethodGet, base+"/activity", nil); status != http.StatusOK || !strings.Contains(activity, "permission_internet_kinds") {
		t.Fatalf("the activity does not show the change: %d", status)
	}
	status, body, _ = fetch(t, noFollow, http.MethodPost, base+"/settings/permissions", url.Values{"permission_internet_untrusted_kinds": {"allow"}})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "<title>Assistant · Settings") {
		t.Fatalf("bounded save = %d: %s", status, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var stored int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings WHERE key = 'permission_internet_untrusted_kinds'").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the bounded key stored %d rows: %v", stored, err)
	}
	if ran := runApp(t, "settings", "set", "permission_people_untrusted_knowledge", "allow"); ran.code == cli.ExitOK {
		t.Fatalf("the CLI accepted allow on a bounded key: %s", ran.stdout)
	} else if !strings.Contains(ran.stderr, "ask, deny") {
		t.Errorf("CLI refusal = %q", ran.stderr)
	}

	srv, pages := webPages(t)
	start := warm(t, api)
	thread := latestThread(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("end_turn", anthropictest.Text("fetched")),
	)
	if status, body, _ := fetch(t, client, http.MethodPost, fmt.Sprintf("%s/chat/%d/messages", base, thread), url.Values{"text": {"read " + srv.URL + "/a"}}); status >= http.StatusBadRequest {
		t.Fatalf("POST message = %d: %s", status, body)
	}
	for deadline := time.Now().Add(20 * time.Second); len(api.Requests()) < start+3; {
		if time.Now().After(deadline) {
			t.Fatalf("%d model requests, want 3", len(api.Requests())-start)
		}
		time.Sleep(20 * time.Millisecond)
	}
	requests := api.Requests()[start:]
	if len(requests) != 3 {
		t.Fatalf("%d model requests, want 3", len(requests))
	}
	if result := toolResultFor(t, requests[len(requests)-1], "f1"); result["is_error"] == true {
		t.Errorf("the allowed fetch failed: %v", result)
	}
	if hits := pages.seen(); len(hits) != 1 || hits[0] != "/a" {
		t.Errorf("page fetches = %v, want /a", hits)
	}
	var proposals int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM proposals WHERE kind = 'permission'").Scan(&proposals); err != nil || proposals != 0 {
		t.Errorf("%d permission requests after internet/kinds was set to allow, want 0: %v", proposals, err)
	}
}
