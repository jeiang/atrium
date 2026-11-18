package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func knowledgeCreate(t *testing.T, w *asstWeb, form url.Values) int64 {
	t.Helper()
	code, body, header := w.post("/library/knowledge/new", form, false)
	if code != http.StatusSeeOther && code != http.StatusFound {
		t.Fatalf("create knowledge record = %d: %s", code, body)
	}
	location := header.Get("Location")
	id, err := strconv.ParseInt(strings.TrimPrefix(location, "/library/knowledge/"), 10, 64)
	if err != nil {
		t.Fatalf("create redirect %q: %v", location, err)
	}
	return id
}

func knowledgeMarks(t *testing.T, d *db.DB, id int64) (marks int64, confirmed, retracted bool) {
	t.Helper()
	var confirmedAt, retractedAt *float64
	if err := d.Read.QueryRow("SELECT marks, confirmed_at, retracted_at FROM kb_records WHERE id = ?", id).
		Scan(&marks, &confirmedAt, &retractedAt); err != nil {
		t.Fatalf("record %d: %v", id, err)
	}
	return marks, confirmedAt != nil, retractedAt != nil
}

func knowledgeCreateG1(t *testing.T, c *webClient, fields map[string]any) int64 {
	t.Helper()
	return number(t, apiData(t, c.do(http.MethodPost, "/web/v1/knowledge", fields), http.StatusCreated)["id"])
}

func TestKnowledgeTabConfirmRetractAndPurge(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := localWebG1(t, base)

	first := knowledgeCreateG1(t, w, map[string]any{
		"title": "Boiler code", "body": "The boiler code is 4417.", "scope": "work",
		"marks": []string{"untrusted", "pii"},
	})
	sibling := knowledgeCreateG1(t, w, map[string]any{
		"title": "Boiler service", "body": "Serviced by Chen in March.", "scope": "work",
		"marks": []string{"untrusted"},
	})
	if _, err := d.Write.Exec("UPDATE kb_records SET source_run_id = 42 WHERE id IN (?, ?)", first, sibling); err != nil {
		t.Fatal(err)
	}
	if record, _ := apiData(t, w.do(http.MethodGet, fmt.Sprintf("/web/v1/knowledge/%d", first), nil), http.StatusOK)["record"].(map[string]any); record["title"] != "Boiler code" {
		t.Fatalf("record read: %v", record)
	}

	apiData(t, w.do(http.MethodPost, fmt.Sprintf("/web/v1/knowledge/%d/confirm", first), nil), http.StatusOK)
	marks, confirmed, retracted := knowledgeMarks(t, d, first)
	if !confirmed || retracted || marks != 2 {
		t.Fatalf("after confirm marks = %d confirmed %v retracted %v, want pii and confirmed", marks, confirmed, retracted)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "kb_retract", map[string]any{"id": first, "reason": "stale"})))
	if ran := runApp(t, "agent", "run", "--text", "retract the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("agent retract run: %d %s", ran.code, ran.stderr)
	}
	var rule, note, status string
	if err := d.Read.QueryRow("SELECT COALESCE(json_extract(preview, '$.change.rule'), ''), COALESCE(json_extract(preview, '$.change.note'), ''), status "+
		"FROM proposals WHERE kind = 'permission' AND json_extract(payload, '$.tool') = 'kb_retract' ORDER BY id DESC LIMIT 1").Scan(&rule, &note, &status); err != nil {
		t.Fatalf("agent retract request: %v", err)
	}
	if !strings.Contains(rule, "confirmed by the operator") || note != "stale" || status != "pending" {
		t.Errorf("agent retract of a confirmed record asked with rule %q, note %q, status %q", rule, note, status)
	}
	if _, _, retracted := knowledgeMarks(t, d, first); retracted {
		t.Fatal("the agent retracted a confirmed record before the operator decided")
	}

	apiData(t, w.do(http.MethodPost, fmt.Sprintf("/web/v1/knowledge/%d/retract", first), nil), http.StatusOK)
	if _, _, retracted := knowledgeMarks(t, d, first); !retracted {
		t.Fatal("record is not retracted")
	}
	apiData(t, w.do(http.MethodPost, "/web/v1/knowledge/retract-run", map[string]any{"run_id": 42}), http.StatusOK)
	if _, _, retracted := knowledgeMarks(t, d, sibling); !retracted {
		t.Fatal("sibling is not retracted after the run retract")
	}
	apiData(t, w.do(http.MethodPost, fmt.Sprintf("/web/v1/knowledge/%d/restore", first), nil), http.StatusOK)
	if _, _, retracted := knowledgeMarks(t, d, first); retracted {
		t.Fatal("record is still retracted after restore")
	}
}

func TestKnowledgePurgeRunsForTheOperator(t *testing.T) {
	t.Parallel()
	s := wimOIDCSite(t)
	s.exec("INSERT INTO kb_records (title, body, scope, writer, created_at, updated_at) VALUES ('Old note', 'secret text', 'personal', 'operator', 1, 1)")
	var id int64
	if err := s.db.Read.QueryRow("SELECT id FROM kb_records WHERE title = 'Old note'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (1, 1, 'legacy-test', 'local', 'kb_write', 'legacy-test', 'forward', ?, '{}', '{\"body\":\"secret text\"}', '{\"body\":\"secret text\"}', '{\"op\":\"kb_restore\"}', 'done', 1)", "kb:"+strconv.FormatInt(id, 10))
	path := fmt.Sprintf("/web/v1/knowledge/%d", id)

	anon := newWebClient(t, s.base, nil)
	if got := anon.do(http.MethodDelete, path, nil); got.status != http.StatusUnauthorized && got.status != http.StatusForbidden {
		t.Fatalf("anonymous purge = %d: %.300s", got.status, got.body)
	}
	if n := s.count("SELECT COUNT(*) FROM kb_records WHERE id = ?", id); n != 1 {
		t.Fatalf("an anonymous purge removed the record")
	}

	browser := s.browser()
	browser.mustSignIn("member")
	operator := newWebClient(t, s.base, nil)
	operator.http = browser.http
	operator.session("oidc")
	if record, _ := apiData(t, operator.do(http.MethodGet, path, nil), http.StatusOK)["record"].(map[string]any); record["title"] != "Old note" {
		t.Fatalf("record read: %v", record)
	}
	if got := operator.do(http.MethodDelete, path, nil); got.status != http.StatusNoContent {
		t.Fatalf("operator purge = %d: %.300s", got.status, got.body)
	}
	if n := s.count("SELECT COUNT(*) FROM kb_records WHERE id = ?", id); n != 0 {
		t.Fatal("purge left the record")
	}
	if n := s.count("SELECT COUNT(*) FROM effects WHERE target = ? AND before_json = '{\"purged\":true}'", "kb:"+strconv.FormatInt(id, 10)); n != 1 {
		t.Fatalf("purge blanked %d earlier snapshots, want 1", n)
	}
	if n := s.count("SELECT COUNT(*) FROM effects WHERE operation = 'kb_purge' AND target = ?", "kb:"+strconv.FormatInt(id, 10)); n != 1 {
		t.Fatalf("purge rows = %d, want one contentless kb_purge row", n)
	}
	for _, read := range []string{"/web/v1/activity", "/web/v1/effects"} {
		if got := operator.do(http.MethodGet, read, nil); got.status != http.StatusOK || strings.Contains(got.body, "secret text") {
			t.Errorf("%s after the purge = %d, still shows the purged text or fails: %.300s", read, got.status, got.body)
		}
	}
}

func TestPinnedConfirmedFactReachesThePrompt(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := localWebG1(t, base)

	knowledgeCreateG1(t, w, map[string]any{"title": "Dentist", "body": "Dr Ng at the Main St clinic; prefers mornings.", "scope": "personal", "pinned": true})
	knowledgeCreateG1(t, w, map[string]any{
		"title": "Rumour", "body": "The clinic moved to Elm St.", "scope": "personal",
		"pinned": true, "marks": []string{"untrusted"},
	})
	profile := apiData(t, w.do(http.MethodGet, "/web/v1/knowledge/profile", nil), http.StatusOK)
	entries, _ := profile["entries"].([]any)
	inPrompt := map[string]bool{}
	var rumourReason string
	for _, e := range entries {
		entry := e.(map[string]any)
		title, _ := entry["title"].(string)
		inPrompt[title] = entry["in_prompt"] == true
		if title == "Rumour" {
			rumourReason, _ = entry["reason"].(string)
		}
	}
	if !inPrompt["Dentist"] || inPrompt["Rumour"] || !strings.Contains(rumourReason, "marked") {
		t.Fatalf("profile = %v, want Dentist in the prompt and Rumour kept out as marked", profile)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	start := len(api.Requests())
	if ran := runApp(t, "agent", "run", "--text", "when is the dentist"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) == 0 {
		t.Fatal("the chat run made no model request")
	}
	system := systemText(t, requests[0])
	if !strings.Contains(system, "Dentist") || !strings.Contains(system, "Dr Ng at the Main St clinic") {
		t.Errorf("system prompt lacks the pinned confirmed fact: %.600s", system)
	}
	if strings.Contains(system, "Rumour") || strings.Contains(system, "Elm St") {
		t.Errorf("system prompt carries a marked record: %.600s", system)
	}
}
