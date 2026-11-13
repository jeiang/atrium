//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDraftSendOnceAndLoops(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mrvMailFixture(t, "shadow")
	smtp := mrvSMTPServer(t)
	if got := runApp(t, "sync"); got.code != 0 {
		t.Fatalf("sync: %s", got.stderr)
	}
	d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := d.Read.QueryRow(query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, subject := range []string{"Lunch plans", "Dinner plans"} {
		exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, revision, created_at, updated_at) VALUES (NULL, 'icloud', 'friend@x.test', ?, 'See you.', 'draft', 1, 1000, 1000)", subject)
	}
	lunch := "/api/v1/drafts/" + scalar("SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lunch plans'")
	dinner := "/api/v1/drafts/" + scalar("SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Dinner plans'")
	message := scalar("SELECT CAST(id AS TEXT) FROM messages WHERE subject = 'Statement'")
	token := localAPIToken(t, "api")
	t.Setenv("ATRIUM_IMAP_ADDR", "127.0.0.1:1")
	logs, synced := &lockedBuffer{}, scalar("SELECT CAST(MAX(updated_ts) AS TEXT) FROM open_loops")
	base, _, stop, err := startServeAppArgs(t, m.app, logs, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	for wait := time.Now().Add(60 * time.Second); scalar("SELECT CAST(COALESCE(MAX(updated_ts), 0) > CAST(? AS REAL) AS TEXT) FROM open_loops", synced) != "1"; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(wait) {
			t.Fatalf("the watch pass never refreshed open loops: %s", logs.String())
		}
	}
	for i := range 120 {
		exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES (?, 'icloud', 'owe_reply', ?, ?, 'pat', 'Dinner?', 150)", fmt.Sprintf("k%d", i), i+1, 100+i)
	}
	exec("INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, updated_ts) VALUES ('w1', 'icloud', 'waiting_on_them', 500, 90, 'sam', 'Contract', 120)")
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (1, ?, 'Send invoice', 'me', '2030-01-02', 'open', 300)", message)
	exec("INSERT INTO packages (id, merchant, status, status_text, tracking_number, source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'Bookshop', 'transit', 'In transit', 'TRACK', ?, ?, 500, 500)", message, message)

	s := &apiSite{t: t, base: base}

	loops := "/api/v1/mail/loops?state=owe_reply&limit=100"
	tag := expectConditional(t, s, token, loops)
	page := apiData(t, s.do(http.MethodGet, loops, token, nil), http.StatusOK)
	first, next := apiItems(t, page), page["next_cursor"]
	if len(first) != 100 || next == nil {
		t.Fatalf("first loop page: %d items, cursor %v", len(first), next)
	}
	cursor := next.(string)
	if rest := apiItems(t, apiData(t, s.do(http.MethodGet, loops+"&cursor="+cursor, token, nil), http.StatusOK)); len(rest) != 21 {
		t.Fatalf("second loop page: %d items", len(rest))
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/mail/loops?state=waiting_on_them&limit=100&cursor="+cursor, token, nil), http.StatusBadRequest, "bad_cursor")
	loopID := strconv.FormatInt(number(t, first[0].(map[string]any)["id"]), 10)
	for days, want := range map[int]float64{0: 1, 999: 60} {
		if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/loops/"+loopID+"/snooze", token, map[string]any{"days": days}), http.StatusOK); got["days"] != want {
			t.Fatalf("snooze %d days: %v, want %v", days, got, want)
		}
	}
	if changed := expectChanged(t, s, token, loops, tag); len(apiItems(t, changed)) != 100 || changed["next_cursor"] == nil {
		t.Fatalf("loops after a snooze: %v", changed["next_cursor"])
	}
	if n := scalar("SELECT CAST(COUNT(*) AS TEXT) FROM loop_overrides WHERE snoozed_until IS NOT NULL"); n != "1" {
		t.Fatalf("snoozed loops: %s", n)
	}
	expectField(t, s.do(http.MethodPost, "/api/v1/mail/loops/dismiss", token, map[string]any{"state": "everything"}), "state")
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/loops/dismiss", token, map[string]any{"state": "waiting_on_them"}), http.StatusOK); got["count"] != float64(1) {
		t.Fatalf("dismiss waiting loops: %v", got)
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/mail/commitments/1/done", token, nil), http.StatusOK)
	if done := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/mail/commitments?view=done", token, nil), http.StatusOK)); len(done) != 1 || scalar("SELECT status FROM commitments WHERE id = 1") != "done" {
		t.Fatalf("done commitments: %v", done)
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/mail/packages/1/dismiss", token, nil), http.StatusOK)
	if active := apiItems(t, apiData(t, s.do(http.MethodGet, "/api/v1/mail/packages?active=true", token, nil), http.StatusOK)); len(active) != 0 {
		t.Fatalf("active packages after dismissing: %v", active)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/packages/999/dismiss", token, nil), http.StatusNotFound, "not_found")

	current := s.do(http.MethodGet, lunch, token, nil)
	rev := number(t, current.json["revision"])
	stale := fmt.Sprintf(`W/"%d"`, rev+7)
	expectAPI(t, s.do(http.MethodPatch, lunch, token, map[string]any{"subject": "Overwritten", "revision": rev + 7}), http.StatusConflict, "stale")
	expectAPI(t, s.do(http.MethodPatch, lunch, token, map[string]any{"subject": "Overwritten"}, "If-Match", stale), http.StatusConflict, "stale")
	expectAPI(t, s.do(http.MethodPost, lunch+"/send", token, map[string]any{}, "If-Match", stale), http.StatusConflict, "stale")
	expectField(t, s.do(http.MethodPost, lunch+"/send", token, map[string]any{}), "revision")
	if n := len(smtp.sent()); n != 0 {
		t.Fatalf("refused sends reached SMTP: %d", n)
	}
	sent := s.do(http.MethodPost, lunch+"/send", token, map[string]any{"revision": rev}, "Idempotency-Key", "lunch-1")
	if sent.status != http.StatusOK || sent.json["draft"].(map[string]any)["status"] != "sent" {
		t.Fatalf("send: %d %s", sent.status, sent.body)
	}
	again := s.do(http.MethodPost, lunch+"/send", token, map[string]any{"revision": rev}, "Idempotency-Key", "lunch-1")
	if again.status != http.StatusOK || again.body != sent.body || again.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("send replay: %d %v %s", again.status, again.header, again.body)
	}
	if n := len(smtp.sent()); n != 1 {
		t.Fatalf("SMTP deliveries after a replayed send: %d", n)
	}
	stop()

	smtp.set("hang")
	p := serveCLIProcess(t, m.dir)
	s = &apiSite{t: t, base: "http://" + p.addr}
	dinnerRev := number(t, s.do(http.MethodGet, dinner, token, nil).json["revision"])
	go func() {
		req, _ := http.NewRequest(http.MethodPost, s.base+dinner+"/send", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "dinner-1")
		req.Header.Set("If-Match", fmt.Sprintf(`W/"%d"`, dinnerRev))
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	deadline := time.Now().Add(60 * time.Second)
	for len(smtp.sent()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the send never reached SMTP: %s", p.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	command := scalar("SELECT id FROM commands WHERE idempotency_key LIKE '%:dinner-1' AND state = 'running'")
	p.stop(t, syscall.SIGKILL)
	smtp.set("")
	exec("UPDATE effects SET lease_at = lease_at - 3600 WHERE state = 'intent'")
	exec("UPDATE drafts SET updated_at = updated_at - 3600 WHERE status = 'sending'")

	p = serveCLIProcess(t, m.dir)
	s = &apiSite{t: t, base: "http://" + p.addr}
	if data := commandStatus(t, s, token, command); data["state"] != "interrupted" {
		t.Fatalf("send command after the crash: %v", data)
	}
	replayed := s.do(http.MethodPost, dinner+"/send", token, map[string]any{}, "Idempotency-Key", "dinner-1", "If-Match", fmt.Sprintf(`W/"%d"`, dinnerRev))
	expectAPI(t, replayed, http.StatusConflict, "command_interrupted")
	after := s.do(http.MethodGet, dinner, token, nil)
	if after.json["status"] != "unknown" {
		t.Fatalf("draft after the crash: %s", after.body)
	}
	if n := scalar("SELECT CAST(COUNT(*) AS TEXT) FROM effects WHERE operation = 'smtp_send' AND state = 'unknown'"); n != "1" {
		t.Fatalf("unknown send effects: %s", n)
	}
	if got := s.do(http.MethodPost, dinner+"/send", token, map[string]any{"revision": number(t, after.json["revision"])}); got.status != http.StatusConflict {
		t.Fatalf("resend of an uncertain draft: %d %s", got.status, got.body)
	}
	if n := len(smtp.sent()); n != 2 {
		t.Fatalf("an uncertain draft was sent again: %d deliveries", n)
	}
	resolved := s.do(http.MethodPost, dinner+"/resolve", token, map[string]any{"outcome": "draft"})
	if resolved.status != http.StatusOK || resolved.json["draft"].(map[string]any)["status"] != "draft" {
		t.Fatalf("resolve: %d %s", resolved.status, resolved.body)
	}
}
