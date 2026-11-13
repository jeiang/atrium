//go:build !modernc

package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
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
