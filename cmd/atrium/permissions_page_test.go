package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestPermissionsPageSavesWithinBounds(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	browser := webG3(t, base)
	okG3(t, "GET permissions", browser.do(http.MethodGet, "/web/v1/permissions", nil))
	saved := okG3(t, "save", browser.do(http.MethodPut, "/web/v1/permissions", map[string]any{"changes": []map[string]string{{"key": "permission_internet_kinds", "value": "allow"}}}))
	if keys, _ := saved["data"].(map[string]any)["keys"].([]any); len(keys) != 1 || keys[0] != "permission_internet_kinds" {
		t.Fatalf("save = %v", saved)
	}
	if activity := okG3(t, "activity", browser.do(http.MethodGet, "/web/v1/activity", nil)); !strings.Contains(fmt.Sprint(activity), "permission_internet_kinds") {
		t.Fatalf("the activity does not show the change: %v", activity)
	}
	expectAPI(t, browser.do(http.MethodPut, "/web/v1/permissions", map[string]any{"changes": []map[string]string{{"key": "permission_internet_untrusted_kinds", "value": "allow"}}}), http.StatusUnprocessableEntity, "invalid")
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
	okG3(t, "POST message", browser.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "read " + srv.URL + "/a"}))
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
