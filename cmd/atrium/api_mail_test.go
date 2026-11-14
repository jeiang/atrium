//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailrun "github.com/jeiang/atrium/internal/mail/run"
)

func TestMailRulesCorrectionAccountsAPI(t *testing.T) {
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mrvMailFixture(t, "shadow")
	if got := runApp(t, "sync"); got.code != 0 {
		t.Fatalf("sync: %s", got.stderr)
	}
	d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var id string
	if err := d.Read.QueryRow("SELECT CAST(id AS TEXT) FROM messages WHERE subject = 'Statement'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	creds := map[string]string{"me@work.test": "work-pass", "atrium.test@icloud.com": "app-pass"}
	strictIMAPRig(t, creds, map[string]*mailtest.FakeClient{"icloud": m.fake, "work": mailtest.NewFakeClient()})
	strictSMTPRig(t, creds)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	s := &apiSite{t: t, base: base}
	rulesPath := "/api/v1/mail/rules"
	expectAPI(t, s.do(http.MethodGet, rulesPath, "", nil, "Remote-User", "owner", "Cookie", "session=forged"), http.StatusUnauthorized, "unauthenticated")
	tag := expectConditional(t, s, token, rulesPath)
	rule := map[string]any{"account": "icloud", "action": "file", "dest": "Bills", "when": []any{map[string]any{"field": "from_addr", "op": "eq", "value": "pay@bank.test"}}}
	created := apiData(t, s.do(http.MethodPost, rulesPath, token, map[string]any{"rule": rule}), http.StatusCreated)
	ruleID := created["id"].(string)
	changed := expectChanged(t, s, token, rulesPath, tag)
	if len(apiItems(t, changed)) == 0 {
		t.Fatal("created rule absent")
	}
	beforeRules, beforeEffects, beforeAudit, beforeCommands := count("SELECT COUNT(*) FROM rules"), count("SELECT COUNT(*) FROM effects"), count("SELECT COUNT(*) FROM operation_audit"), count("SELECT COUNT(*) FROM commands")
	preview := s.do(http.MethodPost, rulesPath+"/preview", token, map[string]any{"rule": rule}, "Idempotency-Key", "preview-only")
	if got := apiData(t, preview, http.StatusOK); got["count"] != float64(1) {
		t.Fatalf("preview: %v", got)
	}
	if preview.header.Get("ETag") != "" || preview.header.Get("Atrium-Command-Id") != "" || preview.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("preview headers: %v", preview.header)
	}
	if count("SELECT COUNT(*) FROM rules") != beforeRules || count("SELECT COUNT(*) FROM effects") != beforeEffects || count("SELECT COUNT(*) FROM operation_audit") != beforeAudit || count("SELECT COUNT(*) FROM commands") != beforeCommands {
		t.Fatal("preview persisted a write")
	}
	correctionPath := "/api/v1/mail/messages/" + id + "/correction"
	expectField(t, s.do(http.MethodPost, correctionPath, token, map[string]any{"folder": "Bills"}), "mode")
	expectField(t, s.do(http.MethodPost, correctionPath, token, map[string]any{"folder": "Bills", "mode": " immediate"}), "mode")
	if got := count("SELECT COUNT(*) FROM corrections"); got != 0 {
		t.Fatalf("a padded mode recorded a correction: %d", got)
	}
	shadow := apiData(t, s.do(http.MethodPost, correctionPath, token, map[string]any{"folder": "Bills", "mode": "immediate"}), http.StatusOK)
	if shadow["outcome"] != "deferred" || len(m.fake.Writes) != 0 || count("SELECT COUNT(*) FROM effects") != beforeEffects {
		t.Fatalf("an immediate correction in shadow mode attempted a move: %v %v", shadow, m.fake.Writes)
	}
	if got := apiData(t, s.do(http.MethodPost, correctionPath, token, map[string]any{"folder": "Bills", "mode": "deferred"}), http.StatusOK); got["outcome"] != "deferred" {
		t.Fatalf("deferred correction outcome: %v", got)
	}
	message := apiData(t, s.do(http.MethodGet, "/api/v1/mail/messages/"+id, token, nil), http.StatusOK)
	if message["corrected_to"] != "Bills" || count("SELECT COUNT(*) FROM effects") != beforeEffects || len(m.fake.Writes) != 0 {
		t.Fatalf("deferred correction wrote mail: %v %v", message, m.fake.Writes)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/mail/messages/999999", token, nil), http.StatusNotFound, "not_found")
	accounts := "/api/v1/mail/accounts"
	apiData(t, s.do(http.MethodGet, accounts, token, nil), http.StatusOK)
	bad := map[string]any{"id": "work", "preset": "gmail", "login": "me@work.test", "password": "wrong-pass"}
	refused := s.do(http.MethodPost, accounts, token, bad)
	expectAPI(t, refused, http.StatusConflict, "refused")
	if strings.Contains(refused.body, "wrong-pass") {
		t.Fatal("account response exposed password")
	}
	if count("SELECT COUNT(*) FROM mail_accounts WHERE id = 'work'") != 0 || count("SELECT COUNT(*) FROM connections WHERE id = 'mail/work'") != 0 || count("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'mail/work'") != 0 {
		t.Fatal("failed account test stored account or credentials")
	}
	expectAPI(t, s.do(http.MethodDelete, accounts+"/icloud", token, nil), http.StatusConflict, "refused")
	if got := apiItems(t, apiData(t, s.do(http.MethodGet, accounts, token, nil), http.StatusOK)); len(got) != 1 || got[0].(map[string]any)["pinned"] != true {
		t.Fatalf("pinned account removed: %v", got)
	}
	bad["password"] = "work-pass"
	apiData(t, s.do(http.MethodPost, accounts, token, bad), http.StatusCreated)
	if got := apiItems(t, apiData(t, s.do(http.MethodGet, accounts+"?account=work", token, nil), http.StatusOK)); len(got) != 1 || got[0].(map[string]any)["state"] != "ok" {
		t.Fatalf("created account: %v", got)
	}
	apiData(t, s.do(http.MethodPatch, accounts+"/work", token, map[string]any{"label": "Work updated", "enabled": true}), http.StatusOK)
	if got := apiData(t, s.do(http.MethodPost, accounts+"/work/test", token, nil), http.StatusOK); got["ok"] != true {
		t.Fatalf("account test: %v", got)
	}
	apiData(t, s.do(http.MethodDelete, accounts+"/work", token, nil), http.StatusOK)
	if got := apiItems(t, apiData(t, s.do(http.MethodGet, accounts+"?account=work", token, nil), http.StatusOK)); len(got) != 0 {
		t.Fatalf("removed account: %v", got)
	}
	apiData(t, s.do(http.MethodPut, rulesPath+"/order", token, map[string]any{"id": ruleID, "account": "icloud", "direction": "up"}), http.StatusOK)
	apiData(t, s.do(http.MethodPatch, rulesPath+"/"+ruleID, token, map[string]any{"rule": rule}), http.StatusOK)
	apiData(t, s.do(http.MethodGet, rulesPath+"/export", token, nil), http.StatusOK)
	apiData(t, s.do(http.MethodGet, "/api/v1/mail/triage", token, nil), http.StatusOK)
	apiData(t, s.do(http.MethodGet, "/api/v1/mail/messages?account=icloud", token, nil), http.StatusOK)
	apiData(t, s.do(http.MethodGet, "/api/v1/mail/live/actions", token, nil), http.StatusOK)
	for i := range 105 {
		if _, err := d.Write.Exec("INSERT INTO rules (position,id,account,action,dest,conditions) VALUES (?,?,'icloud','file','Bills','[]')", i+100, fmt.Sprintf("api-page-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	first := apiData(t, s.do(http.MethodGet, rulesPath+"?account=icloud&limit=100", token, nil), http.StatusOK)
	next, _ := first["next_cursor"].(string)
	if len(apiItems(t, first)) != 100 || next == "" {
		t.Fatalf("first page: %v", first)
	}
	second := apiData(t, s.do(http.MethodGet, rulesPath+"?account=icloud&limit=100&cursor="+next, token, nil), http.StatusOK)
	if len(apiItems(t, second)) != 6 || second["next_cursor"] != nil {
		t.Fatalf("second page: %v", second)
	}
	expectAPI(t, s.do(http.MethodGet, rulesPath+"?account=other&cursor="+next, token, nil), http.StatusBadRequest, "bad_cursor")
	apiData(t, s.do(http.MethodDelete, rulesPath+"/"+ruleID, token, nil), http.StatusOK)
	for _, document := range []string{`[{"id":"imported","account":"icloud","action":"file","dest":"Bills","when":[{"field":"from_addr","op":"eq","value":"pay@bank.test"}]}]`, `{"rules":[{"id":"imported","account":"icloud","action":"file","dest":"Bills","when":[{"field":"from_addr","op":"eq","value":"pay@bank.test"}]}]}`} {
		body := "--rules\r\nContent-Disposition: form-data; name=\"upload\"; filename=\"rules.json\"\r\nContent-Type: application/json\r\n\r\n" + document + "\r\n--rules--\r\n"
		if got := apiData(t, s.do(http.MethodPost, rulesPath+"/import", token, body, "Content-Type", "multipart/form-data; boundary=rules"), http.StatusOK); got["count"] != float64(1) {
			t.Fatalf("import: %v", got)
		}
		if got := apiItems(t, apiData(t, s.do(http.MethodGet, rulesPath, token, nil), http.StatusOK)); len(got) != 1 || got[0].(map[string]any)["id"] != "imported" {
			t.Fatalf("imported rules: %v", got)
		}
	}
	livePath := "/api/v1/mail/live"
	liveTag := expectConditional(t, s, token, livePath)
	apiData(t, s.do(http.MethodPost, livePath+"/pause", token, nil), http.StatusOK)
	if got := expectChanged(t, s, token, livePath, liveTag); got["paused"] != true || got["mode"] != "shadow" {
		t.Fatalf("pause: %v", got)
	}
	stop()
	seedSettings(t, map[string]string{mail.KeyMode: "live"})
	if got := runApp(t, "live", "rebaseline"); got.code != 0 {
		t.Fatalf("rebaseline: %s", got.stderr)
	}
	beforePausedEffects := count("SELECT COUNT(*) FROM effects")
	base, _, stop, err = startServeAppArgs(t, m.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	s.base = base
	paused := apiData(t, s.do(http.MethodPost, correctionPath, token, map[string]any{"folder": "Bills", "mode": "immediate"}), http.StatusOK)
	if n := count("SELECT COUNT(*) FROM effects"); paused["outcome"] != "deferred" || len(m.fake.Writes) != 0 || n != beforePausedEffects {
		t.Fatalf("paused live correction: writes %v, effects %d -> %d", m.fake.Writes, beforePausedEffects, n)
	}
	apiData(t, s.do(http.MethodPost, livePath+"/resume", token, nil), http.StatusOK)
	if got := apiData(t, s.do(http.MethodGet, livePath, token, nil), http.StatusOK); got["paused"] != false {
		t.Fatalf("resume: %v", got)
	}
}

func TestMailPassesAPI(t *testing.T) {
	const from = "statements@bank.test"
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mailSafetyFixture(t, "live")
	m.add("Bills", "Old statement", from, "old", "Your statement is ready.", `\Seen`)
	m.run(t, "sync")
	rulesFile := filepath.Join(m.dir, "rules.json")
	if err := os.WriteFile(rulesFile, []byte(`{"rules":[{"id":"old","account":"icloud","action":"delete","when":[{"field":"from_addr","op":"eq","value":"`+from+`"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", rulesFile)
	d := mustConnect(t, m.path, keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	token := localAPIToken(t, "api")
	arrived, release := make(chan struct{}), make(chan struct{})
	var arrivedOnce, releaseOnce sync.Once
	var hooked sync.Mutex
	statusCalls := 0
	m.fake.SetStatusHook(func(_ string, n uint32) uint32 {
		hooked.Lock()
		statusCalls++
		hooked.Unlock()
		arrivedOnce.Do(func() {
			close(arrived)
			<-release
		})
		return n
	})
	logs := &lockedBuffer{}
	base, _, stop, err := startServeAppArgs(t, m.app, logs, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s := &apiSite{t: t, base: base}
	select {
	case <-arrived:
	case <-time.After(60 * time.Second):
		t.Fatalf("the watch pass never reached IMAP: %s", logs.String())
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/sync", "", nil), http.StatusUnauthorized, "unauthenticated")
	fetches, syncs := len(m.fake.Fetches), count("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.sync'")
	busy := s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{}, "Idempotency-Key", "sync-1")
	expectAPI(t, busy, http.StatusServiceUnavailable, "refused")
	if busy.header.Get("Retry-After") == "" {
		t.Fatalf("busy sync without Retry-After: %v", busy.header)
	}
	hooked.Lock()
	calls := statusCalls
	hooked.Unlock()
	if calls != 1 || len(m.fake.Fetches) != fetches {
		t.Fatalf("a busy sync reached IMAP: %d status calls, fetches %d -> %d", calls, fetches, len(m.fake.Fetches))
	}
	if n := count("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.sync'"); n != syncs {
		t.Fatalf("a busy sync was audited as a run: %d -> %d", syncs, n)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/scans/rebalance", token, nil), http.StatusServiceUnavailable, "refused")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/loops/refresh", token, nil, "Idempotency-Key", "refresh-1"), http.StatusServiceUnavailable, "refused")
	m.fake.SetStatusHook(nil)
	releaseOnce.Do(func() { close(release) })
	waitUntil(t, 60*time.Second, "the watch pass to release mail-pass", func() bool {
		got := s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{}, "Idempotency-Key", "sync-1")
		if got.status == http.StatusServiceUnavailable && got.code() == "refused" {
			return false
		}
		if data := apiData(t, got, http.StatusOK); data["outcome"] != "ok" || got.header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("sync retried under the busy key: %v %v", data, got.header)
		}
		return true
	})
	if refreshed := s.do(http.MethodPost, "/api/v1/mail/loops/refresh", token, nil, "Idempotency-Key", "refresh-1"); refreshed.status != http.StatusOK || refreshed.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("refresh retried under the busy key: %d %v %s", refreshed.status, refreshed.header, refreshed.body)
	}
	if data := apiData(t, s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{"account": "icloud"}), http.StatusOK); data["outcome"] != "ok" {
		t.Fatalf("sync: %v", data)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{"account": "nope"}), http.StatusUnprocessableEntity, "invalid")

	writes, fetches, searches, models := len(m.fake.Writes), len(m.fake.Fetches), len(m.fake.Searches), len(m.api.Requests())
	rows := func() string {
		t.Helper()
		var out []string
		for _, table := range []string{"operation_audit", "effects", "messages", "decisions", "live_baseline", "proposals", "job_runs"} {
			out = append(out, table+"="+strconv.Itoa(count("SELECT COUNT(*) FROM "+table)))
		}
		return strings.Join(out, " ")
	}
	liveState := func() string { return s.do(http.MethodGet, "/api/v1/mail/live", token, nil).body }
	beforeRows, beforeLive := rows(), liveState()
	replayed := apiData(t, s.do(http.MethodPost, "/api/v1/mail/replay", token, map[string]any{"account": "icloud"}), http.StatusOK)
	if summary, _ := replayed["summary"].(map[string]any); summary == nil || summary["messages"] == nil {
		t.Fatalf("replay: %v", replayed)
	}
	if got := s.do(http.MethodPost, "/api/v1/mail/replay", token, map[string]any{"model": true}); got.status/100 != 4 {
		t.Fatalf("replay with the paid model mode: %d %s", got.status, got.body)
	}
	if len(m.fake.Writes) != writes || len(m.fake.Fetches) != fetches || len(m.fake.Searches) != searches || len(m.api.Requests()) != models {
		t.Fatalf("replay made external calls: writes %d->%d fetches %d->%d searches %d->%d model %d->%d", writes, len(m.fake.Writes), fetches, len(m.fake.Fetches), searches, len(m.fake.Searches), models, len(m.api.Requests()))
	}
	if after := rows(); after != beforeRows {
		t.Fatalf("replay changed stored rows: %s -> %s", beforeRows, after)
	}
	if after := liveState(); after != beforeLive {
		t.Fatalf("replay changed the live account state: %s -> %s", beforeLive, after)
	}

	apply := "/api/v1/mail/live/deletions/apply"
	refusedAudits := func() int {
		return count("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.apply-deletions' AND outcome = 'refused'")
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/pause", token, nil), http.StatusOK)
	opens, refusals := m.fake.Opens(), refusedAudits()
	paused := s.do(http.MethodPost, apply, token, nil)
	expectAPI(t, paused, http.StatusConflict, "refused")
	if len(m.fake.Writes) != writes || len(m.fake.Boxes["Bills"].Box.Messages) != 1 || m.fake.Opens() != opens {
		t.Fatalf("paused deletions reached IMAP: %d sessions, wrote %v", m.fake.Opens()-opens, m.verbs()[writes:])
	}
	if n := refusedAudits(); n != refusals+1 {
		t.Fatalf("paused deletions audited %d refusals, want %d", n, refusals+1)
	}
	apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/resume", token, nil), http.StatusOK)
	done := s.do(http.MethodPost, apply, token, nil, "Idempotency-Key", "apply-1")
	data := apiData(t, done, http.StatusOK)
	ids, _ := done.json["effect_ids"].([]any)
	accounts, _ := data["accounts"].([]any)
	if data["outcome"] != "ok" || len(ids) != 1 || len(accounts) != 1 || accounts[0].(map[string]any)["trashed"] != float64(1) {
		t.Fatalf("apply deletions: %s", done.body)
	}
	if state := count("SELECT COUNT(*) FROM effects WHERE id = ? AND origin = 'mail_auto' AND operation = 'trash' AND state = 'done'", number(t, ids[0])); state != 1 {
		t.Fatalf("trash effect %v not done", ids[0])
	}
	log := m.fake.Writes[writes:]
	if len(log) != 3 || log[0].Verb != "COPY" || log[1].Verb != "STORE" || !strings.Contains(strings.Join(log[1].Args, " "), `\Deleted`) || log[2].Verb != "EXPUNGE" || len(m.fake.Boxes["Bills"].Box.Messages) != 0 {
		t.Fatalf("apply deletions wrote %+v, want COPY, STORE \\Deleted, UID EXPUNGE", log)
	}
	writes, opens = len(m.fake.Writes), m.fake.Opens()
	audits, effectRows := count("SELECT COUNT(*) FROM operation_audit"), count("SELECT COUNT(*) FROM effects")
	replay := s.do(http.MethodPost, apply, token, nil, "Idempotency-Key", "apply-1")
	if replay.status != http.StatusOK || replay.header.Get("Idempotent-Replayed") == "" || replay.body != done.body {
		t.Fatalf("keyed second apply was not the stored result: %d %v %s", replay.status, replay.header, replay.body)
	}
	if m.fake.Opens() != opens || len(m.fake.Writes) != writes || count("SELECT COUNT(*) FROM operation_audit") != audits || count("SELECT COUNT(*) FROM effects") != effectRows {
		t.Fatalf("keyed replay ran a pass: %d sessions, writes %v", m.fake.Opens()-opens, m.verbs()[writes:])
	}
	if again := apiData(t, s.do(http.MethodPost, apply, token, nil), http.StatusOK); again["outcome"] != "ok" || len(m.fake.Writes) != writes {
		t.Fatalf("second deletion pass: %v, writes %v", again, m.verbs()[writes:])
	}

	for _, scan := range []string{"rebalance", "deletions", "gmail-labels"} {
		if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/scans/"+scan, token, nil), http.StatusOK); got["outcome"] != "ok" {
			t.Fatalf("scan %s: %v", scan, got)
		}
	}
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/rebaseline", token, nil), http.StatusOK); got["outcome"] != "ok" {
		t.Fatalf("rebaseline: %v", got)
	}
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/backlog", token, nil), http.StatusOK); got["outcome"] != "ok" {
		t.Fatalf("backlog: %v", got)
	}
	if got := apiData(t, s.do(http.MethodGet, "/api/v1/mail/shadow-report?since=7d", token, nil), http.StatusOK); !strings.Contains(fmt.Sprint(got["report"]), "icloud") {
		t.Fatalf("shadow report: %v", got)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/mail/shadow-report?since=bogus", token, nil), http.StatusUnprocessableEntity, "invalid")
	if len(m.fake.Writes) != writes {
		t.Fatalf("scans, rebaseline or backlog wrote %v", m.verbs()[writes:])
	}
}

func TestMailApplyDeletionsAPIRefusedInShadow(t *testing.T) {
	const from = "statements@bank.test"
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mailSafetyFixture(t, "shadow")
	m.add("Bills", "Old statement", from, "old", "Your statement is ready.", `\Seen`)
	m.run(t, "sync")
	rulesFile := filepath.Join(m.dir, "rules.json")
	if err := os.WriteFile(rulesFile, []byte(`{"rules":[{"id":"old","account":"icloud","action":"delete","when":[{"field":"from_addr","op":"eq","value":"`+from+`"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, "rules", "import", rulesFile)
	d := mustConnect(t, m.path, keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	token := localAPIToken(t, "api")
	logs := &lockedBuffer{}
	base, _, stop, err := startServeAppArgs(t, m.app, logs, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	s := &apiSite{t: t, base: base}
	waitUntil(t, 60*time.Second, "mail-pass to be free", func() bool {
		return s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{}).status == http.StatusOK
	})
	syncRun := func() string {
		t.Helper()
		var out string
		if err := d.Read.QueryRow("SELECT COALESCE(CAST(j.last_run_id AS TEXT), '') || ':' || COALESCE(r.status, '') || ':' || COALESCE(r.error, '') FROM jobs j LEFT JOIN job_runs r ON r.id = j.last_run_id WHERE j.declaration_key = ?", mailrun.SyncJobKey).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := syncRun()
	opens, writes := m.fake.Opens(), len(m.fake.Writes)
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/live/deletions/apply", token, nil, "Idempotency-Key", "shadow-1"), http.StatusConflict, "refused")
	if after := syncRun(); after != before {
		t.Fatalf("a refused API apply changed the sync job's last run: %s -> %s", before, after)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{"account": "nope"}), http.StatusUnprocessableEntity, "invalid")
	if got := syncRun(); !strings.Contains(got, ":ok:") {
		t.Fatalf("an API sync with an unknown account recorded the sync run as %s", got)
	}
	if strings.Contains(logs.String(), "crashed") {
		t.Fatalf("API mail commands logged a crashed job: %s", logs.String())
	}
	if m.fake.Opens() != opens || len(m.fake.Writes) != writes || len(m.fake.Boxes["Bills"].Box.Messages) != 1 {
		t.Fatalf("shadow deletions reached IMAP: %d sessions, wrote %v", m.fake.Opens()-opens, m.verbs()[writes:])
	}
	if n := count("SELECT COUNT(*) FROM operation_audit WHERE operation = 'mail.apply-deletions' AND outcome = 'refused'"); n != 1 {
		t.Fatalf("shadow deletions audited %d refusals", n)
	}
	if n := count("SELECT COUNT(*) FROM effects WHERE operation = 'trash'"); n != 0 {
		t.Fatalf("shadow deletions wrote %d trash intents", n)
	}
}

func TestMailSyncAPIDrainsOnShutdown(t *testing.T) {
	const from = "statements@bank.test"
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mailSafetyFixture(t, "live")
	m.add("Bills", "Old statement", from, "old", "Your statement is ready.", `\Seen`)
	m.run(t, "sync")
	d := mustConnect(t, m.path, keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	token := localAPIToken(t, "api")
	logs := &lockedBuffer{}
	base, _, stop, err := startServeAppArgs(t, m.app, logs, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	s := &apiSite{t: t, base: base}
	waitUntil(t, 60*time.Second, "mail-pass to be free", func() bool {
		return s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{}).status == http.StatusOK
	})
	running := "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? AND r.status = 'running' AND r.owner LIKE 'manual/%'"
	arrived, release := make(chan struct{}), make(chan struct{})
	var arrivedOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	m.fake.SetStatusHook(func(_ string, n uint32) uint32 {
		var claimed int
		if err := d.Read.QueryRow(running, mailrun.SyncJobKey).Scan(&claimed); err != nil || claimed != 1 {
			return n
		}
		arrivedOnce.Do(func() {
			close(arrived)
			<-release
		})
		return n
	})
	m.add("Bills", "New statement", from, "new", "Another statement.", "")
	replied := make(chan apiReply, 1)
	deadline := time.After(60 * time.Second)
	for waiting := true; waiting; {
		go func() {
			replied <- s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{}, "Idempotency-Key", "shutdown-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		}()
		select {
		case <-arrived:
			waiting = false
		case <-replied:
		case <-deadline:
			t.Fatalf("the API sync never reached IMAP: %s", logs.String())
		}
	}
	if n := count(running, mailrun.SyncJobKey); n != 1 {
		t.Fatalf("the API sync is not a claimed %s run: %d running", mailrun.SyncJobKey, n)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		releaseOnce.Do(func() { close(release) })
	}()
	stop()
	select {
	case <-replied:
	case <-time.After(15 * time.Second):
		t.Fatal("the API sync outlived serve")
	}
	if n := count(running, mailrun.SyncJobKey); n != 0 {
		t.Fatalf("the API sync run was left running: %d", n)
	}
	if n := count("SELECT COUNT(*) FROM effects e WHERE e.state IN ('intent', 'unknown') AND NOT EXISTS (SELECT 1 FROM effect_resolutions r WHERE r.effect_id = e.id)"); n != 0 {
		t.Fatalf("shutdown left %d unsettled effects", n)
	}
}
