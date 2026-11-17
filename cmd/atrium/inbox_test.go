package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestInboxListsNotificationsAndSettingsKeepsOnlyChannels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	seedAllFlags(t)
	base := startServeIn(t, dir)
	client := localWebG5(t, base)
	if got := client.do(http.MethodGet, "/web/v1/inbox", nil); got.status != http.StatusOK {
		t.Fatalf("empty inbox = %d: %.300s", got.status, got.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO notifications (id, ts, title, body, channel, delivered, error) VALUES (7, 1, 'Parcel arrives today', 'Locker 4', 'assistant', 0, 'no push devices')"); err != nil {
		t.Fatal(err)
	}
	got := client.do(http.MethodGet, "/web/v1/inbox", nil)
	if got.status != http.StatusOK {
		t.Fatalf("inbox = %d: %.300s", got.status, got.body)
	}
	inbox := got.body
	for _, want := range []string{"Parcel arrives today", "Locker 4"} {
		if !strings.Contains(inbox, want) {
			t.Errorf("inbox misses %q", want)
		}
	}
	got = client.do(http.MethodGet, "/web/v1/notifications", nil)
	if got.status != http.StatusOK {
		t.Fatalf("settings = %d: %.300s", got.status, got.body)
	}
	settings := got.body
	for _, unwanted := range []string{"Parcel arrives today"} {
		if strings.Contains(settings, unwanted) {
			t.Errorf("settings still lists %q", unwanted)
		}
	}
	if got := client.do(http.MethodPost, "/settings/notifications/7/resend", nil); got.status != http.StatusNotFound && got.status != http.StatusMethodNotAllowed {
		t.Errorf("old resend route = %d", got.status)
	}
	if got := client.do(http.MethodPost, "/web/v1/inbox/99/resend", nil); got.status != http.StatusNotFound {
		t.Errorf("unknown resend = %d", got.status)
	}
	var target, outcome string
	if err := d.Read.QueryRow("SELECT target,outcome FROM operation_audit WHERE operation = 'notifications.resend' ORDER BY id DESC LIMIT 1").Scan(&target, &outcome); err != nil || target != "notification:99" || outcome != "not_found" {
		t.Fatalf("resend audit = %s %s, %v", target, outcome, err)
	}
}

func TestInboxPageAndAPIShareBounds(t *testing.T) {
	t.Parallel()
	site := newAPISite(t)
	owner := site.token("owner")
	d := mustConnect(t, filepath.Join(site.dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	clearBootNotices(t, d)
	tx, err := d.Write.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 120; i++ {
		if _, err := tx.Exec("INSERT INTO notifications (ts,title,body,channel,delivered) VALUES (?,?,?,'assistant',0)", i, fmt.Sprintf("Bounded notice %03d", i), "bounded body"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	newestBounded := func(what string, items []any, want int) {
		t.Helper()
		next := 120
		for _, item := range items {
			title, _ := item.(map[string]any)["title"].(string)
			if !strings.HasPrefix(title, "Bounded notice ") {
				if next != 120 {
					t.Fatalf("%s: %q among the bounded notices", what, title)
				}
				continue
			}
			if title != fmt.Sprintf("Bounded notice %03d", next) {
				t.Fatalf("%s: %q, want Bounded notice %03d", what, title, next)
			}
			next--
		}
		if len(items) != want {
			t.Fatalf("%s: %d items, want %d", what, len(items), want)
		}
	}
	page := site.do(http.MethodGet, "/api/v1/inbox", owner, nil)
	if page.status != http.StatusOK {
		t.Fatalf("default inbox = %d %s", page.status, page.body)
	}
	items, _ := page.json["items"].([]any)
	newestBounded("default inbox", items, 50)
	more := site.do(http.MethodGet, "/api/v1/inbox?limit=100", owner, nil)
	if more.status != http.StatusOK {
		t.Fatalf("bounded inbox = %d %s", more.status, more.body)
	}
	items, _ = more.json["items"].([]any)
	newestBounded("bounded inbox", items, 100)
	expectAPI(t, site.do(http.MethodGet, "/api/v1/inbox?limit=500", owner, nil), http.StatusUnprocessableEntity, "invalid")
	web := proxyWebG5(t, site).do(http.MethodGet, "/web/v1/inbox?limit=100", nil)
	if web.status != http.StatusOK {
		t.Fatalf("web inbox = %d %s", web.status, web.body)
	}
	items, _ = web.json["items"].([]any)
	newestBounded("web inbox", items, 100)
}

func TestNotificationOperationAuditsTrustedRun(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	result := toolResult(t, "notify", map[string]any{"title": "Audit notification", "body": "private notification body", "thread_id": 999999, "run_id": 999999})
	if !strings.Contains(result, `"recorded": true`) && !strings.Contains(result, `"recorded":true`) {
		t.Fatalf("notify tool = %s", result)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var source, outcome, details, target string
	var id, thread, run, nthread, nrun int64
	if err := d.Read.QueryRow("SELECT id,thread_id,run_id FROM notifications WHERE title='Audit notification'").Scan(&id, &nthread, &nrun); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT source,outcome,thread_id,run_id,details_json,target FROM operation_audit WHERE operation='notifications.send' AND target=?", fmt.Sprintf("notification:%d", id)).Scan(&source, &outcome, &thread, &run, &details, &target); err != nil {
		t.Fatal(err)
	}
	if source != "tool" || outcome != "failed" || thread == 0 || run == 0 || thread != nthread || run != nrun || thread == 999999 || run == 999999 || strings.Contains(details, "private notification body") || !strings.HasPrefix(target, "notification:") {
		t.Fatalf("notification provenance = %q %q %d/%d vs %d/%d, %s %s", source, outcome, thread, run, nthread, nrun, target, details)
	}
}

func TestNotificationOperationKeepsRunAndNightlyCaps(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"chat", "nightly"} {
		t.Run(kind, func(t *testing.T) {
			dir, _, _ := moduleFixture(t, flagsExcept("", ""))
			var calls []json.RawMessage
			for i := 0; i < 4; i++ {
				calls = append(calls, anthropictest.ToolUse(fmt.Sprintf("notify%d", i), "notify", map[string]any{"title": fmt.Sprintf("Cap notice %d", i), "body": "bounded"}))
			}
			server := anthropictest.New(t, anthropictest.Reply("tool_use", calls...), anthropictest.Reply("end_turn", anthropictest.Text("done")))
			setEnv(t, anthropic.BaseURLEnv, server.URL)
			ran := runApp(t, "agent", "run", "--kind", kind, "--text", "notify me")
			if ran.code != cli.ExitOK {
				t.Fatalf("%s notify = %d %s", kind, ran.code, ran.stderr)
			}
			d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
			defer func() { _ = d.Close() }()
			want := 3
			if kind == "nightly" {
				want = 1
			}
			var notices, refused, audits int
			if err := d.Read.QueryRow("SELECT count(*) FROM notifications WHERE title LIKE 'Cap notice %'").Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if err := d.Read.QueryRow("SELECT count(*),coalesce(sum(outcome='refused'),0) FROM operation_audit WHERE operation='notifications.send' AND source='tool'").Scan(&audits, &refused); err != nil {
				t.Fatal(err)
			}
			if notices != want || audits != 4 || refused != 4-want {
				t.Fatalf("%s caps: %d notices, %d audits, %d refused", kind, notices, audits, refused)
			}
		})
	}
}
