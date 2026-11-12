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

func TestKnowledgeTabConfirmRetractAndPurge(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	// A marked thread cannot call kb_write on this base, so the marked record is
	// created through the operator form's mark checkboxes.
	first := knowledgeCreate(t, w, url.Values{
		"title": {"Boiler code"}, "body": {"The boiler code is 4417."}, "scope": {"work"},
		"untrusted": {"1"}, "pii": {"1"},
	})
	sibling := knowledgeCreate(t, w, url.Values{
		"title": {"Boiler service"}, "body": {"Serviced by Chen in March."}, "scope": {"work"},
		"untrusted": {"1"},
	})
	if _, err := d.Write.Exec("UPDATE kb_records SET source_run_id = 42 WHERE id IN (?, ?)", first, sibling); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := w.get(fmt.Sprintf("/library/knowledge/%d", first), false); code != http.StatusOK || !strings.Contains(body, "Boiler code") {
		t.Fatalf("record page = %d: %.300s", code, body)
	}

	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d/confirm", first), url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("confirm = %d: %s", code, body)
	}
	marks, confirmed, retracted := knowledgeMarks(t, d, first)
	if !confirmed || retracted || marks != 2 {
		t.Fatalf("after confirm marks = %d confirmed %v retracted %v, want pii and confirmed", marks, confirmed, retracted)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "kb_retract", map[string]any{"id": first, "reason": "stale"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("cannot")))
	if ran := runApp(t, "agent", "run", "--text", "retract the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("agent retract run: %d %s", ran.code, ran.stderr)
	}
	var refusal string
	if err := d.Read.QueryRow("SELECT COALESCE(error, '') FROM tool_calls WHERE name = 'kb_retract' ORDER BY id DESC LIMIT 1").Scan(&refusal); err != nil {
		t.Fatalf("agent retract call: %v", err)
	}
	if !strings.Contains(refusal, "confirmed by the operator") {
		t.Errorf("agent retract of a confirmed record = %q, want a refusal", refusal)
	}

	code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d/retract", first), url.Values{}, true)
	if code != http.StatusOK || !strings.Contains(body, fmt.Sprintf("/library/knowledge/%d", sibling)) ||
		!strings.Contains(body, "/library/knowledge/runs/42/retract") {
		t.Fatalf("retract = %d, want the sibling and the run action: %.500s", code, body)
	}
	if _, _, retracted := knowledgeMarks(t, d, first); !retracted {
		t.Fatal("record is not retracted")
	}
	if code, body, _ := w.post("/library/knowledge/runs/42/retract", url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("run retract = %d: %s", code, body)
	}
	if _, _, retracted := knowledgeMarks(t, d, sibling); !retracted {
		t.Fatal("sibling is not retracted after the run retract")
	}
	if code, body, _ := w.post(fmt.Sprintf("/library/knowledge/%d/restore", first), url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("restore = %d: %s", code, body)
	}
	if _, _, retracted := knowledgeMarks(t, d, first); retracted {
		t.Fatal("record is still retracted after restore")
	}
}

func TestKnowledgePurgeRunsForTheOperator(t *testing.T) {
	s := wimOIDCSite(t)
	s.exec("INSERT INTO kb_records (title, body, scope, writer, created_at, updated_at) VALUES ('Old note', 'secret text', 'personal', 'operator', 1, 1)")
	var id int64
	if err := s.db.Read.QueryRow("SELECT id FROM kb_records WHERE title = 'Old note'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (1, 1, 'legacy-test', 'local', 'kb_write', 'legacy-test', 'forward', ?, '{}', '{\"body\":\"secret text\"}', '{\"body\":\"secret text\"}', '{\"op\":\"kb_restore\"}', 'done', 1)", "kb:"+strconv.FormatInt(id, 10))

	anon := s.browser()
	wimExpect(t, anon.post(fmt.Sprintf("/library/knowledge/%d/purge", id), nil), http.StatusSeeOther)
	if n := s.count("SELECT COUNT(*) FROM kb_records WHERE id = ?", id); n != 1 {
		t.Fatalf("an anonymous purge removed the record")
	}

	operator := s.browser()
	operator.mustSignIn("member")
	wimExpect(t, operator.get(fmt.Sprintf("/library/knowledge/%d", id)), http.StatusOK, "Old note")
	reply := operator.post(fmt.Sprintf("/library/knowledge/%d/purge", id), nil)
	if reply.status != http.StatusSeeOther {
		t.Fatalf("operator purge = %d: %.300s", reply.status, reply.body)
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
	activity := operator.get("/activity")
	if strings.Contains(activity.body, "secret text") {
		t.Error("activity still shows the purged text")
	}
}

func TestPinnedConfirmedFactReachesThePrompt(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	if code, body, _ := w.post("/library/knowledge/profile", url.Values{
		"title": {"Dentist"}, "body": {"Dr Ng at the Main St clinic; prefers mornings."},
	}, false); code != http.StatusSeeOther {
		t.Fatalf("add fact = %d: %s", code, body)
	}
	knowledgeCreate(t, w, url.Values{
		"title": {"Rumour"}, "body": {"The clinic moved to Elm St."}, "scope": {"personal"},
		"pinned": {"1"}, "untrusted": {"1"},
	})
	if code, body, _ := w.get("/library/knowledge/profile", false); code != http.StatusOK ||
		!strings.Contains(body, "In prompt") || !strings.Contains(body, "marked") {
		t.Fatalf("profile page = %d, want In prompt and marked: %.400s", code, body)
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
