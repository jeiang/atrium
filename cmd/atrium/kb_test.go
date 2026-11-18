package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func kbMarks(t *testing.T, d *db.DB, threadID int64) int64 {
	t.Helper()
	var marks int64
	if err := d.Read.QueryRow("SELECT marks FROM threads WHERE id = ?", threadID).Scan(&marks); err != nil {
		t.Fatalf("thread %d marks: %v", threadID, err)
	}
	return marks
}

func kbResult(t *testing.T, d *db.DB, threadID int64, toolUseID string) map[string]any {
	t.Helper()
	rows, err := d.Read.Query("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role != 'assistant' ORDER BY seq DESC", threadID)
	if err != nil {
		t.Fatalf("thread %d messages: %v", threadID, err)
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var content string
		err := r.Scan(&content)
		return content, err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range found {
		var blocks []map[string]any
		if err := json.Unmarshal([]byte(content), &blocks); err != nil {
			continue
		}
		for _, block := range blocks {
			if block["type"] != "tool_result" || block["tool_use_id"] != toolUseID {
				continue
			}
			body, _ := block["content"].(string)
			var out map[string]any
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				t.Fatalf("tool result %s body %q: %v", toolUseID, body, err)
			}
			return out
		}
	}
	t.Fatalf("thread %d has no result for %s", threadID, toolUseID)
	return nil
}

func kbResults(t *testing.T, d *db.DB, threadID int64, toolUseID string) []any {
	t.Helper()
	results, _ := kbResult(t, d, threadID, toolUseID)["results"].([]any)
	return results
}

func TestKnowledgeWrittenInAMarkedThreadMarksItsReader(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("k1", "kb_write", map[string]any{
		"title": "Dentist", "body": "Dr Ng at the Main St clinic; prefers mornings.", "scope": "personal",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--text", "remember the dentist"); ran.code != cli.ExitOK {
		t.Fatalf("kb_write run: %d %s", ran.code, ran.stderr)
	}
	writer := latestThread(t, dir)

	var id, marks int64
	var writerName string
	var source *int64
	if err := d.Read.QueryRow("SELECT id, marks, writer, source_thread_id FROM kb_records").Scan(&id, &marks, &writerName, &source); err != nil {
		t.Fatalf("record after kb_write: %v", err)
	}
	if marks != kbMarks(t, d, writer) || marks != 0 {
		t.Errorf("record marks = %d, writer thread marks = %d; want the writer's marks", marks, kbMarks(t, d, writer))
	}
	if writerName != "chat" || source == nil || *source != writer {
		t.Errorf("record writer %q source %v, want chat from thread %d", writerName, source, writer)
	}
	var writeID int64
	var toolName, target, undo string
	if err := d.Read.QueryRow("SELECT id, operation, target, undo_json FROM effects WHERE direction = 'forward' ORDER BY id DESC LIMIT 1").
		Scan(&writeID, &toolName, &target, &undo); err != nil {
		t.Fatalf("write row: %v", err)
	}
	if toolName != "kb_write" || target != fmt.Sprintf("kb:%d", id) || !strings.Contains(undo, `"op":"kb_restore"`) {
		t.Errorf("write = %s %s %s, want kb_write kb:%d kb_restore", toolName, target, undo, id)
	}

	message := seedMail(t, dir)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("read")))
	if ran := runApp(t, "agent", "run", "--text", "read my latest mail"); ran.code != cli.ExitOK {
		t.Fatalf("mail run: %d %s", ran.code, ran.stderr)
	}
	marked := latestThread(t, dir)
	if got := kbMarks(t, d, marked); got == 0 {
		t.Fatalf("thread after mail_read is unmarked, want the read's marks")
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("k2", "kb_write", map[string]any{
		"title": "Plumber", "body": "Sam, 0412 345 678.", "scope": "personal",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(marked, 10), "--text", "remember the plumber"); ran.code != cli.ExitOK {
		t.Fatalf("kb_write in the marked thread: %d %s", ran.code, ran.stderr)
	}
	var markedID, markedMarks int64
	if err := d.Read.QueryRow("SELECT id, marks FROM kb_records WHERE title = 'Plumber'").Scan(&markedID, &markedMarks); err != nil {
		t.Fatalf("record from the marked thread: %v", err)
	}
	if want := kbMarks(t, d, marked); markedMarks != want {
		t.Errorf("record from the marked thread marks = %d, want the writer's %d", markedMarks, want)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s3", "kb_search", map[string]any{"query": "plumber"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("found")))
	if ran := runApp(t, "agent", "run", "--text", "who is the plumber"); ran.code != cli.ExitOK {
		t.Fatalf("kb_search of the marked record: %d %s", ran.code, ran.stderr)
	}
	readerOfMarked := latestThread(t, dir)
	if got, want := kbMarks(t, d, readerOfMarked), kbMarks(t, d, marked)|8; got != want {
		t.Errorf("reader of the marked record marks = %d, want the writer's marks plus personal (%d)", got, want)
	}
	if found := kbResults(t, d, readerOfMarked, "s3"); len(found) != 1 {
		t.Errorf("kb_search of the marked record returned %v, want one", found)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "dentist"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("found")))
	if ran := runApp(t, "agent", "run", "--text", "what do I know about the dentist"); ran.code != cli.ExitOK {
		t.Fatalf("kb_search run: %d %s", ran.code, ran.stderr)
	}
	reader := latestThread(t, dir)
	if got := kbMarks(t, d, reader); got != 8 {
		t.Errorf("reader thread marks = %d, want personal", got)
	}
	found := kbResults(t, d, reader, "s1")
	if len(found) != 1 {
		t.Fatalf("kb_search returned %d records, want 1: %v", len(found), found)
	}
	hit, _ := found[0].(map[string]any)
	if hit["cite"] != fmt.Sprintf("K%d", id) || hit["title"] != "Dentist" {
		t.Errorf("kb_search hit = %v, want K%d Dentist", hit, id)
	}
	if semantic, _ := kbResult(t, d, reader, "s1")["semantic"].(bool); semantic {
		t.Error("kb_search reported semantic results without an embedder")
	}

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	browser := localWebG5(t, base)
	activity := browser.do(http.MethodGet, "/web/v1/activity?limit=100", nil)
	if activity.status != http.StatusOK || !activityHasEffectG5(activity, "kb_write", writeID) {
		t.Fatalf("activity = %d, want write %d: %.400s", activity.status, writeID, activity.body)
	}
	if undone := browser.do(http.MethodPost, fmt.Sprintf("/web/v1/effects/%d/undo", writeID), nil); undone.status != http.StatusOK {
		t.Fatalf("undo %d = %d %.400s", writeID, undone.status, undone.body)
	}
	var retracted *float64
	if err := d.Read.QueryRow("SELECT retracted_at FROM kb_records WHERE id = ?", id).Scan(&retracted); err != nil || retracted == nil {
		t.Fatalf("record after undo retracted_at = %v, %v; want a retraction", retracted, err)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s2", "kb_search", map[string]any{"query": "dentist"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing")))
	if ran := runApp(t, "agent", "run", "--text", "dentist again"); ran.code != cli.ExitOK {
		t.Fatalf("second kb_search run: %d %s", ran.code, ran.stderr)
	}
	if found := kbResults(t, d, latestThread(t, dir), "s2"); len(found) != 0 {
		t.Errorf("kb_search after undo returned %v, want nothing", found)
	}
}

func TestKnowledgeSearchSkipsExpiredRecords(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	for _, row := range []struct {
		title   string
		expires float64
	}{{"Locksmith", 1}, {"Glazier", 4102444800}} {
		if _, err := d.Write.Exec("INSERT INTO kb_records (title, body, scope, writer, expires_at, created_at, updated_at) VALUES (?, 'Fixes the broken window.', 'work', 'worker', ?, 1, 1)", row.title, row.expires); err != nil {
			t.Fatal(err)
		}
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "window"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("found")))
	if ran := runApp(t, "agent", "run", "--text", "who fixes the window"); ran.code != cli.ExitOK {
		t.Fatalf("kb_search run: %d %s", ran.code, ran.stderr)
	}
	found := kbResults(t, d, latestThread(t, dir), "s1")
	if len(found) != 1 {
		t.Fatalf("kb_search returned %v, want only the unexpired record", found)
	}
	if hit, _ := found[0].(map[string]any); hit["title"] != "Glazier" {
		t.Fatalf("kb_search hit = %v, want Glazier", hit)
	}
}

func TestPrivateSpaceKnowledgeStaysInItsSpace(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	browser := localWebG5(t, base)
	created := browser.do(http.MethodPost, "/web/v1/knowledge/spaces", map[string]any{"name": "Household", "effort": "medium", "context_limit": 90000, "private": true})
	if created.status != http.StatusCreated {
		t.Fatalf("create project = %d: %s", created.status, created.body)
	}
	projectID := changedIDG5(t, created)
	var private int64
	if err := d.Read.QueryRow("SELECT private FROM projects WHERE id = ?", projectID).Scan(&private); err != nil || private != 1 {
		t.Fatalf("project private = %d, %v; want 1", private, err)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("project thread warm run: %d %s", ran.code, ran.stderr)
	}
	inside := latestThread(t, dir)
	if moved := browser.do(http.MethodPut, fmt.Sprintf("/web/v1/threads/%d/project", inside), map[string]any{"project_id": projectID}); moved.status >= http.StatusBadRequest {
		t.Fatalf("move chat into the project = %d: %s", moved.status, moved.body)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("k1", "kb_write", map[string]any{
		"title": "Boiler code", "body": "The boiler code is 4417.", "scope": "work",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(inside, 10), "--text", "remember the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("kb_write in the project: %d %s", ran.code, ran.stderr)
	}
	var recordID int64
	var recordProject *int64
	if err := d.Read.QueryRow("SELECT id, project_id FROM kb_records WHERE origin IS NULL").Scan(&recordID, &recordProject); err != nil || recordProject == nil || *recordProject != projectID {
		t.Fatalf("record project = %v, %v; want %d", recordProject, err, projectID)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "boiler"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing")))
	if ran := runApp(t, "agent", "run", "--text", "boiler code outside"); ran.code != cli.ExitOK {
		t.Fatalf("outside search: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("outside thread: %d %s", ran.code, ran.stderr)
	}
	outside := latestThread(t, dir)
	var before int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE thread_id = ?", outside).Scan(&before); err != nil {
		t.Fatal(err)
	}
	refused := browser.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", outside), map[string]any{"text": fmt.Sprintf("see @thread/%d", inside)})
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("reference to a private project's thread = %d, want 422: %s", refused.status, refused.body)
	}
	var stored int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE thread_id = ?", outside).Scan(&stored); err != nil || stored != before {
		t.Errorf("refused reference stored %d messages (%v), want the %d already there", stored, err, before)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("g1", "kb_get", map[string]any{"ids": []any{recordID}})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing")))
	if ran := runApp(t, "agent", "run", "--text", "get the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("outside get: %d %s", ran.code, ran.stderr)
	}
	if results := kbResults(t, d, latestThread(t, dir), "g1"); len(results) != 0 {
		t.Errorf("outside kb_get returned %d records, want none", len(results))
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("g2", "kb_get", map[string]any{"ids": []any{recordID}})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("got it")))
	if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(inside, 10), "--text", "get the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("inside get: %d %s", ran.code, ran.stderr)
	}
	results := kbResults(t, d, inside, "g2")
	if len(results) != 1 {
		t.Fatalf("inside kb_get returned %d records, want one", len(results))
	}
	if got, _ := results[0].(map[string]any); got["body"] != "The boiler code is 4417." {
		t.Errorf("inside kb_get body = %v", got["body"])
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "kb_retract", map[string]any{"id": recordID, "reason": "stale"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("cannot")))
	if ran := runApp(t, "agent", "run", "--text", "retract the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("outside retract: %d %s", ran.code, ran.stderr)
	}
	var refusal string
	if err := d.Read.QueryRow("SELECT COALESCE(error, '') FROM tool_calls WHERE name = 'kb_retract' ORDER BY id DESC LIMIT 1").Scan(&refusal); err != nil {
		t.Fatalf("outside retract call: %v", err)
	}
	if !strings.Contains(refusal, "belongs to another project") {
		t.Errorf("outside retract = %q, want a project refusal", refusal)
	}
	var retracted sql.NullFloat64
	if err := d.Read.QueryRow("SELECT retracted_at FROM kb_records WHERE id = ?", recordID).Scan(&retracted); err != nil || retracted.Valid {
		t.Fatalf("outside retract changed the record: %v %v", retracted, err)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r2", "kb_retract", map[string]any{"id": recordID, "reason": "stale"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("retracted")))
	if ran := runApp(t, "agent", "run", "--thread", strconv.FormatInt(inside, 10), "--text", "retract the boiler code"); ran.code != cli.ExitOK {
		t.Fatalf("inside retract: %d %s", ran.code, ran.stderr)
	}
	if err := d.Read.QueryRow("SELECT retracted_at FROM kb_records WHERE id = ?", recordID).Scan(&retracted); err != nil || !retracted.Valid {
		t.Fatalf("inside retract left the record live: %v %v", retracted, err)
	}
}

func TestKnowledgeListRevision(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := localWebG5(t, base)

	list := w.do(http.MethodGet, "/web/v1/knowledge", nil)
	empty := list.header.Get("ETag")
	if list.status != http.StatusOK || !strings.HasPrefix(empty, `W/"`) {
		t.Fatalf("empty list = %d etag %q: %.300s", list.status, empty, list.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO read_revisions(scope, value) VALUES ('effects', 1), ('assistant', 1), ('settings', 1) ON CONFLICT(scope) DO UPDATE SET value = value + 1"); err != nil {
		t.Fatal(err)
	}
	if again := w.do(http.MethodGet, "/web/v1/knowledge", nil, "If-None-Match", empty); again.status != http.StatusNotModified || again.body != "" {
		t.Fatalf("unchanged list with its etag = %d: %.300s", again.status, again.body)
	}

	knowledgeCreateG5(t, w, map[string]any{"title": "Gate code", "body": "The gate code is 2231.", "scope": "work"})
	changed := w.do(http.MethodGet, "/web/v1/knowledge", nil, "If-None-Match", empty)
	if changed.status != http.StatusOK || !strings.Contains(changed.body, "Gate code") {
		t.Fatalf("list after a create with the old etag = %d: %.300s", changed.status, changed.body)
	}
	if fresh := changed.header.Get("ETag"); fresh == "" || fresh == empty {
		t.Fatalf("etag after a create = %q, was %q", fresh, empty)
	}

	if bogus := w.do(http.MethodGet, "/web/v1/knowledge?scope=bogus", nil); bogus.status != http.StatusUnprocessableEntity || !strings.Contains(bogus.body, "scope") {
		t.Fatalf("bogus scope = %d: %.300s", bogus.status, bogus.body)
	}
}

func knowledgeAudits(t *testing.T, d *db.DB, operation string, id int64, outcome string) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = ? AND target = ? AND outcome = ? AND source = 'api'",
		operation, strconv.FormatInt(id, 10), outcome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKnowledgeOperatorLifecycleAudits(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := localWebG5(t, base)
	body := func(id int64) string {
		t.Helper()
		var b string
		if err := d.Read.QueryRow("SELECT body FROM kb_records WHERE id = ?", id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	id := knowledgeCreateG5(t, w, map[string]any{"title": "Water shutoff", "body": "Under the stairs.", "scope": "personal"})
	path := fmt.Sprintf("/web/v1/knowledge/%d", id)
	if edited := w.do(http.MethodPatch, path, map[string]any{"title": "Water shutoff", "body": "Behind the boiler.", "scope": "personal"}); edited.status != http.StatusOK {
		t.Fatalf("edit = %d: %.300s", edited.status, edited.body)
	}
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'kb_edit' AND target = ?", "kb:"+strconv.FormatInt(id, 10)).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10)); got.code != cli.ExitOK {
		t.Fatalf("undo the edit: %d %s", got.code, got.stderr)
	}
	if got := body(id); got != "Under the stairs." {
		t.Fatalf("body after undoing the edit = %q", got)
	}
	for _, action := range []string{"confirm", "retract", "restore"} {
		if got := w.do(http.MethodPost, path+"/"+action, nil); got.status != http.StatusOK {
			t.Fatalf("%s = %d: %.300s", action, got.status, got.body)
		}
	}

	if invalid := w.do(http.MethodPatch, path, map[string]any{"title": "", "body": "Draft that must not land.", "scope": "personal"}); invalid.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid edit = %d: %.300s", invalid.status, invalid.body)
	}
	if got := body(id); got != "Under the stairs." {
		t.Fatalf("an invalid edit changed the body to %q", got)
	}
	if purged := w.do(http.MethodDelete, path, nil); purged.status != http.StatusNoContent {
		t.Fatalf("purge = %d: %.300s", purged.status, purged.body)
	}

	for _, op := range []string{"knowledge.create", "knowledge.edit", "knowledge.confirm", "knowledge.retract", "knowledge.restore", "knowledge.purge"} {
		if n := knowledgeAudits(t, d, op, id, "ok"); n != 1 {
			t.Errorf("%s ok audit rows = %d, want 1", op, n)
		}
	}
	if n := knowledgeAudits(t, d, "knowledge.edit", id, "invalid"); n != 1 {
		t.Errorf("invalid edit audit rows = %d, want 1", n)
	}
}

func TestKnowledgeConfirmMatchingReportsCommittedPrefix(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	seed := func(title, scope string, retracted bool) int64 {
		t.Helper()
		var retractedAt any
		if retracted {
			retractedAt = 2
		}
		res, err := d.Write.Exec("INSERT INTO kb_records (title, body, scope, marks, writer, retracted_at, created_at, updated_at) VALUES (?, 'x', ?, 1, 'import', ?, 1, 1)",
			title, scope, retractedAt)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	matching := []int64{seed("Boiler", "personal", false), seed("Fuse box", "personal", false), seed("Gutters", "personal", false)}
	retracted := seed("Old alarm code", "personal", true)
	outside := seed("Desk number", "work", false)
	confirmed := func(id int64) bool {
		t.Helper()
		var at sql.NullFloat64
		if err := d.Read.QueryRow("SELECT confirmed_at FROM kb_records WHERE id = ?", id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.Valid
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := localWebG5(t, base)

	batch := w.do(http.MethodPost, "/web/v1/knowledge/confirm-matching", map[string]any{"scope": "personal", "state": "imported"})
	data, _ := batch.json["data"].(map[string]any)
	committed, _ := data["committed"].([]any)
	refusedRows, _ := data["refused"].([]any)
	if batch.status != http.StatusOK || len(committed) != 3 || len(refusedRows) != 1 {
		t.Fatalf("confirm-all = %d: %.300s", batch.status, batch.body)
	}
	for _, id := range matching {
		if !confirmed(id) || knowledgeAudits(t, d, "knowledge.confirm-matching", id, "ok") != 1 {
			t.Errorf("record %d not confirmed with one ok audit row", id)
		}
	}
	if confirmed(retracted) || knowledgeAudits(t, d, "knowledge.confirm-matching", retracted, "refused") != 1 {
		t.Errorf("retracted record %d confirmed or not audited as refused", retracted)
	}
	if confirmed(outside) {
		t.Errorf("out-of-scope record %d was confirmed", outside)
	}

	later := seed("Smoke alarm", "personal", false)
	selected := w.do(http.MethodPost, "/web/v1/knowledge/confirm", map[string]any{"ids": []int64{outside, retracted, later}})
	if selected.status != http.StatusOK || !strings.Contains(selected.body, fmt.Sprintf(`"refused":[{"id":%d,`, retracted)) {
		t.Fatalf("confirm-selected past a retracted record = %d, want the retracted record refused: %.300s", selected.status, selected.body)
	}
	if !confirmed(outside) || confirmed(later) {
		t.Errorf("confirm-selected: first record confirmed = %v, record after the refusal confirmed = %v", confirmed(outside), confirmed(later))
	}
}
