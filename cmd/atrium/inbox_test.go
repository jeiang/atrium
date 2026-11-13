package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if code, body, _ := fetch(t, client, http.MethodGet, base+"/inbox", nil); code != http.StatusOK || strings.Contains(body, `action="/inbox/`) {
		t.Fatalf("empty inbox = %d: %.300s", code, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO notifications (id, ts, title, body, channel, delivered, error) VALUES (7, 1, 'Parcel arrives today', 'Locker 4', 'assistant', 0, 'no push devices')"); err != nil {
		t.Fatal(err)
	}
	code, inbox, _ := fetch(t, client, http.MethodGet, base+"/inbox", nil)
	if code != http.StatusOK {
		t.Fatalf("inbox = %d: %.300s", code, inbox)
	}
	for _, want := range []string{"Parcel arrives today", "Locker 4", `action="/inbox/7/resend"`} {
		if !strings.Contains(inbox, want) {
			t.Errorf("inbox misses %q", want)
		}
	}
	code, settings, _ := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
	if code != http.StatusOK {
		t.Fatalf("settings = %d: %.300s", code, settings)
	}
	for _, unwanted := range []string{"Parcel arrives today", "/resend"} {
		if strings.Contains(settings, unwanted) {
			t.Errorf("settings still lists %q", unwanted)
		}
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/settings/notifications/7/resend", url.Values{}); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("old resend route = %d", code)
	}
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/inbox/99/resend", url.Values{}); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown resend = %d", code)
	}
	var operation, outcome string
	if err := d.Read.QueryRow("SELECT operation,outcome FROM operation_audit ORDER BY id DESC LIMIT 1").Scan(&operation, &outcome); err != nil || operation != "notifications.resend" || outcome != "refused" {
		t.Fatalf("resend audit = %s %s, %v", operation, outcome, err)
	}
}

func TestInboxPageAndAPIShareBounds(t *testing.T) {
	site := newAPISite(t)
	owner := site.token("owner")
	d := mustConnect(t, filepath.Join(site.dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for i := 1; i <= 120; i++ {
		if _, err := d.Write.Exec("INSERT INTO notifications (ts,title,body,channel,delivered) VALUES (?,?,?,'assistant',0)", i, fmt.Sprintf("Bounded notice %03d", i), "bounded body"); err != nil {
			t.Fatal(err)
		}
	}
	page := site.do(http.MethodGet, "/api/v1/inbox", owner, nil)
	items, _ := page.json["items"].([]any)
	if page.status != http.StatusOK || len(items) != 50 || number(t, items[0].(map[string]any)["id"]) != 120 {
		t.Fatalf("default inbox = %d %s", page.status, page.body)
	}
	more := site.do(http.MethodGet, "/api/v1/inbox?limit=100", owner, nil)
	items, _ = more.json["items"].([]any)
	if more.status != http.StatusOK || len(items) != 100 {
		t.Fatalf("bounded inbox = %d %s", more.status, more.body)
	}
	expectAPI(t, site.do(http.MethodGet, "/api/v1/inbox?limit=500", owner, nil), http.StatusUnprocessableEntity, "invalid")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req, _ := http.NewRequest(http.MethodGet, site.base+"/inbox", nil)
	req.Header.Set("Remote-User", "owner")
	req.Header.Set("Remote-Groups", "atrium_access")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || strings.Count(string(body), "Bounded notice ") != 100 || strings.Contains(string(body), "Bounded notice 020") || !strings.Contains(string(body), "Bounded notice 021") {
		t.Fatalf("HTML bound = %d, notices %d", resp.StatusCode, strings.Count(string(body), "Bounded notice "))
	}
}

func TestNotificationOperationAuditsTrustedRun(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	result := toolResult(t, "notify", map[string]any{"title": "Audit notification", "body": "private notification body", "thread_id": 999999, "run_id": 999999})
	if !strings.Contains(result, `"recorded": true`) && !strings.Contains(result, `"recorded":true`) {
		t.Fatalf("notify tool = %s", result)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var source, outcome, details, target string
	var thread, run, nthread, nrun int64
	if err := d.Read.QueryRow("SELECT source,outcome,thread_id,run_id,details_json,target FROM operation_audit WHERE operation='notifications.send'").Scan(&source, &outcome, &thread, &run, &details, &target); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT thread_id,run_id FROM notifications WHERE title='Audit notification'").Scan(&nthread, &nrun); err != nil {
		t.Fatal(err)
	}
	if source != "tool" || outcome != "failed" || thread == 0 || run == 0 || thread != nthread || run != nrun || thread == 999999 || run == 999999 || strings.Contains(details, "private notification body") || !strings.HasPrefix(target, "notification:") {
		t.Fatalf("notification provenance = %q %q %d/%d vs %d/%d, %s %s", source, outcome, thread, run, nthread, nrun, target, details)
	}
}

func TestNotificationOperationKeepsRunAndNightlyCaps(t *testing.T) {
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
