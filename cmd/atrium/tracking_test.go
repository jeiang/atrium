package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/notify"
)

type fakeShip24 struct {
	mu        sync.Mutex
	creates   int
	results   int
	milestone string
	refuse    bool
}

func (f *fakeShip24) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.refuse || r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/public/v1/trackers":
		_, _ = w.Write([]byte(`{"data":{"trackers":[]}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/public/v1/trackers":
		f.creates++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"tracker":{"trackerId":"trk_1","trackingNumber":"1Z999AA10123456784"}}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/public/v1/trackers/trk_1/results":
		f.results++
		event := `{"eventId":"e1","status":"In transit","occurrenceDatetime":"2026-11-08T10:00:00","location":"Hub","statusMilestone":"in_transit"}`
		if f.milestone == "delivered" {
			event = `{"eventId":"e2","status":"Delivered","occurrenceDatetime":"2026-11-08T12:00:00","location":"Door","statusMilestone":"delivered"}`
		}
		_, _ = fmt.Fprintf(w, `{"data":{"trackings":[{"tracker":{"trackerId":"trk_1","trackingNumber":"1Z999AA10123456784"},`+
			`"shipment":{"statusMilestone":%q,"statusCode":"x"},"events":[%s]}]}}`, f.milestone, event)
	case r.Method == http.MethodPatch && r.URL.Path == "/public/v1/trackers/trk_1":
		_, _ = w.Write([]byte(`{"data":{"tracker":{"trackerId":"trk_1"}}}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeShip24) setRefuse(value bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = value
}

func (f *fakeShip24) setMilestone(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.milestone = value
}

func (f *fakeShip24) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.results
}

func latestThreadContent(t *testing.T, d *db.DB) string {
	t.Helper()
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

func TestShip24IntegrationTestRecordsOkAndRefusesA401(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, settingEnv(tracking.KeyShip24APIKey), "good")
	s := &integrationSite{t: t, dir: dir}
	s.start()
	if code, body := s.post("/ship24/test", url.Values{"url": {srv.URL + "/public/v1"}}); code != http.StatusOK {
		t.Fatalf("Ship24 Test: %d %s", code, body)
	}
	if health(t, dir, "ship24") != integrations.OK {
		t.Fatalf("Ship24 health = %s, want ok", health(t, dir, "ship24"))
	}
	fake.setRefuse(true)
	if code, body := s.post("/ship24/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "test failed (needs_reauth)") {
		t.Fatalf("refused Ship24 Test: %d %s", code, body)
	}
	if health(t, dir, "ship24") != integrations.NeedsReauth {
		t.Fatalf("health after the refused Test = %s, want needs_reauth", health(t, dir, "ship24"))
	}
	fake.setRefuse(false)
	if code, body := s.post("/ship24/test", nil); code != http.StatusOK {
		t.Fatalf("Ship24 Test after recovery: %d %s", code, body)
	}
	if health(t, dir, "ship24") != integrations.OK {
		t.Fatalf("health after recovery = %s, want ok", health(t, dir, "ship24"))
	}
}

func TestTrackPackageRegistersAndRespectsTheQuota(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, settingEnv(tracking.KeyShip24URL), srv.URL+"/public/v1")
	setEnv(t, settingEnv(tracking.KeyShip24APIKey), "good")
	setEnv(t, settingEnv(tracking.KeyMonthlyQuota), "1")
	if warm := runApp(t, "agent", "run", "--text", "warm"); warm.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", warm.code, warm.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	for i, number := range []string{"1Z999AA10123456784", "1Z999AA10123456785"} {
		if _, err := d.Write.Exec("INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, "+
			"source_message_id, last_message_id, updated_ts, first_seen) VALUES (?, 'UPS', ?, 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, 100, 100)",
			i+1, number); err != nil {
			t.Fatal(err)
		}
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "track_package", map[string]any{"package_id": 1})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "track one", "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("track run: %d %s", ran.code, ran.stderr)
	}
	content := latestThreadContent(t, d)
	for _, want := range []string{"trk_1", "in_transit"} {
		if !strings.Contains(content, want) {
			t.Fatalf("track result misses %q: %s", want, content)
		}
	}
	var tracker, aggregator string
	var registered float64
	if err := d.Read.QueryRow("SELECT tracker_id, aggregator, tracker_registered_ts FROM packages WHERE id = 1").Scan(&tracker, &aggregator, &registered); err != nil {
		t.Fatal(err)
	}
	if tracker != "trk_1" || aggregator != "ship24" || registered <= 0 {
		t.Fatalf("package after tracking: tracker %q aggregator %q registered %v", tracker, aggregator, registered)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t2", "track_package", map[string]any{"package_id": 2})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "track two", "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("second track run: %d %s", ran.code, ran.stderr)
	}
	content = latestThreadContent(t, d)
	if !strings.Contains(content, "quota") {
		t.Fatalf("the exhausted quota was not refused: %s", content)
	}
	if creates, _ := fake.counts(); creates != 1 {
		t.Fatalf("Ship24 created %d trackers, want 1", creates)
	}
	var undo string
	if err := d.Read.QueryRow("SELECT undo_json FROM agent_writes WHERE tool = 'track_package' ORDER BY id DESC LIMIT 1").Scan(&undo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(undo, "package_untrack") {
		t.Fatalf("the tracker write has no undo: %s", undo)
	}
}

func TestPackagePollingUpdatesStatusAndNotifiesOnceOnDelivered(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	fake := &fakeShip24{milestone: "in_transit"}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	setEnv(t, settingEnv(tracking.KeyShip24URL), srv.URL+"/public/v1")
	setEnv(t, settingEnv(tracking.KeyShip24APIKey), "good")
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(ntfy.Close)
	setEnv(t, settingEnv(notify.KeyURL), ntfy.URL)
	if warm := runApp(t, "settings", "list"); warm.code != cli.ExitOK {
		t.Fatalf("warm command: %d %s", warm.code, warm.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, "+
		"source_message_id, last_message_id, updated_ts, first_seen, aggregator, tracker_id) "+
		"VALUES (1, 'UPS', '1Z999AA10123456784', 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, ?, ?, 'ship24', 'trk_1')", now, now); err != nil {
		t.Fatal(err)
	}
	poll := func() {
		t.Helper()
		if ran := runApp(t, "packages", "poll"); ran.code != cli.ExitOK {
			t.Fatalf("package poll: exit %d stderr %s", ran.code, ran.stderr)
		}
	}
	poll()
	var status string
	if err := d.Read.QueryRow("SELECT status FROM packages WHERE id = 1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "in_transit" {
		t.Fatalf("status after the first poll = %q, want in_transit", status)
	}
	var events int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM package_events WHERE package_id = 1").Scan(&events); err != nil || events != 1 {
		t.Fatalf("events after the first poll = %d, %v; want 1", events, err)
	}
	fake.setMilestone("delivered")
	poll()
	if err := d.Read.QueryRow("SELECT status FROM packages WHERE id = 1").Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("status after delivery = %q, %v", status, err)
	}
	var notifications int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications WHERE title = 'Package delivered'").Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("delivered notifications = %d, %v; want 1", notifications, err)
	}
	_, before := fake.counts()
	poll()
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications WHERE title = 'Package delivered'").Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("delivered notifications after a third poll = %d, %v; want 1", notifications, err)
	}
	if _, after := fake.counts(); after != before {
		t.Fatalf("a delivered package was polled again (%d -> %d)", before, after)
	}
}
