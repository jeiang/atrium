package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"regexp"
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
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, page, _ := fetch(t, client, http.MethodGet, base+"/activity", nil); code != http.StatusOK ||
		!strings.Contains(page, fmt.Sprintf("/activity/effect/%d/undo", writeID)) {
		t.Fatalf("activity = %d, want an undo for write %d: %.400s", code, writeID, page)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, fmt.Sprintf("%s/activity/effect/%d/undo", base, writeID), url.Values{}); code != http.StatusOK {
		t.Fatalf("undo %d = %d %.400s", writeID, code, body)
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

func TestPrivateProjectKnowledgeStaysInItsProject(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, body, _ := fetch(t, client, http.MethodGet, base+"/chat", nil); code != http.StatusOK {
		t.Fatalf("GET /chat = %d: %s", code, body)
	}
	post := func(path string, form url.Values) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		for _, c := range client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out), resp.Header
	}

	code, body, header := post("/projects", url.Values{"name": {"Household"}, "effort": {"medium"}, "context_limit": {"90000"}, "private": {"1"}})
	if code != http.StatusNoContent && code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatalf("create project = %d: %s", code, body)
	}
	location := header.Get("HX-Redirect")
	if location == "" {
		location = header.Get("Location")
	}
	projectID, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(location, "/projects/"), "/edit"), 10, 64)
	if err != nil {
		t.Fatalf("project redirect %q: %v", location, err)
	}
	var private int64
	if err := d.Read.QueryRow("SELECT private FROM projects WHERE id = ?", projectID).Scan(&private); err != nil || private != 1 {
		t.Fatalf("project private = %d, %v; want 1", private, err)
	}

	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("project thread warm run: %d %s", ran.code, ran.stderr)
	}
	inside := latestThread(t, dir)
	if code, body, _ := post(fmt.Sprintf("/chat/%d/project", inside), url.Values{"project_id": {strconv.FormatInt(projectID, 10)}}); code >= http.StatusBadRequest {
		t.Fatalf("move chat into the project = %d: %s", code, body)
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
	if err := d.Read.QueryRow("SELECT id, project_id FROM kb_records").Scan(&recordID, &recordProject); err != nil || recordProject == nil || *recordProject != projectID {
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
	code, body, _ = post(fmt.Sprintf("/chat/%d/messages", outside), url.Values{
		"text": {fmt.Sprintf("see @thread/%d", inside)},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("reference to a private project's thread = %d, want 422: %s", code, body)
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
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)

	code, body, header := w.get("/library/knowledge", false)
	empty := header.Get("ETag")
	if code != http.StatusOK || !strings.HasPrefix(empty, `W/"`) {
		t.Fatalf("empty list = %d etag %q: %.300s", code, empty, body)
	}
	if code, body, _ = w.get("/library/knowledge", false, "If-None-Match", empty); code != http.StatusNotModified || body != "" {
		t.Fatalf("unchanged list with its etag = %d: %.300s", code, body)
	}

	knowledgeCreate(t, w, url.Values{"title": {"Gate code"}, "body": {"The gate code is 2231."}, "scope": {"work"}})
	code, body, header = w.get("/library/knowledge", false, "If-None-Match", empty)
	if code != http.StatusOK || !strings.Contains(body, "Gate code") {
		t.Fatalf("list after a create with the old etag = %d: %.300s", code, body)
	}
	if fresh := header.Get("ETag"); fresh == "" || fresh == empty {
		t.Fatalf("etag after a create = %q, was %q", fresh, empty)
	}

	code, body, _ = w.get("/library/knowledge?scope=bogus", false)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "scope must be work or personal") {
		t.Fatalf("bogus scope = %d: %.300s", code, body)
	}
}

func TestKnowledgeRevisionCookieReset(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	a := asstBrowse(t, base)
	_, oldPage, header := a.get("/library/knowledge/profile", false)
	b := asstBrowse(t, base)
	b.client.Jar, _ = cookiejar.New(nil)
	b.token = ""
	code, page, fresh := b.get("/library/knowledge/profile", false, "If-None-Match", header.Get("ETag"))
	if code != http.StatusOK || fresh.Get("ETag") == header.Get("ETag") {
		t.Fatalf("reset cookie conditional GET = %d, etag %q", code, fresh.Get("ETag"))
	}
	tokenPattern := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	oldToken, newToken := tokenPattern.FindStringSubmatch(oldPage), tokenPattern.FindStringSubmatch(page)
	if len(oldToken) != 2 || len(newToken) != 2 || oldToken[1] == newToken[1] {
		t.Fatal("reset page did not render a fresh form token")
	}
	if code, _, fragment := a.get("/library/knowledge/profile", true, "If-None-Match", header.Get("ETag")); code != http.StatusOK || fragment.Get("ETag") == header.Get("ETag") {
		t.Fatalf("fragment reused full-page validator: %d", code)
	}
	code, page, _ = b.post("/library/knowledge/new", url.Values{
		"csrf_token": {newToken[1]}, "title": {"Fresh token"}, "body": {"Saved with a native form."}, "scope": {"personal"},
	}, false)
	if code != http.StatusSeeOther {
		t.Fatalf("native form with fresh token = %d: %.300s", code, page)
	}
}

func knowledgeAudits(t *testing.T, d *db.DB, operation string, id int64, outcome string) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = ? AND target = ? AND outcome = ? AND source = 'ui'",
		operation, strconv.FormatInt(id, 10), outcome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKnowledgeOperatorLifecycleAudits(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	w := asstBrowse(t, base)
	body := func(id int64) string {
		t.Helper()
		var b string
		if err := d.Read.QueryRow("SELECT body FROM kb_records WHERE id = ?", id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	id := knowledgeCreate(t, w, url.Values{"title": {"Water shutoff"}, "body": {"Under the stairs."}, "scope": {"personal"}})
	path := fmt.Sprintf("/library/knowledge/%d", id)
	if code, b, _ := w.post(path, url.Values{"title": {"Water shutoff"}, "body": {"Behind the boiler."}, "scope": {"personal"}}, false); code != http.StatusSeeOther {
		t.Fatalf("edit = %d: %.300s", code, b)
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
		if code, b, _ := w.post(path+"/"+action, url.Values{}, false); code != http.StatusSeeOther {
			t.Fatalf("%s = %d: %.300s", action, code, b)
		}
	}

	code, page, _ := w.post(path, url.Values{"title": {""}, "body": {"Draft that must not land."}, "scope": {"personal"}}, false)
	if code != http.StatusUnprocessableEntity || !strings.Contains(page, "Draft that must not land.") {
		t.Fatalf("invalid edit = %d: %.300s", code, page)
	}
	if got := body(id); got != "Under the stairs." {
		t.Fatalf("an invalid edit changed the body to %q", got)
	}
	if code, b, _ := w.post(path+"/purge", url.Values{}, false); code != http.StatusSeeOther {
		t.Fatalf("purge = %d: %.300s", code, b)
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
	w := asstBrowse(t, base)

	code, page, _ := w.post("/library/knowledge/confirm-all", url.Values{"scope": {"personal"}, "state": {"imported"}}, false)
	if code != http.StatusOK || !strings.Contains(page, "3 confirmed, 1 refused") {
		t.Fatalf("confirm-all = %d: %.300s", code, page)
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
	code, _, _ = w.post("/library/knowledge/confirm", url.Values{"scope": {"personal"}, "state": {"imported"},
		"id": {strconv.FormatInt(outside, 10), strconv.FormatInt(retracted, 10), strconv.FormatInt(later, 10)}}, false)
	if code != http.StatusConflict {
		t.Fatalf("confirm-selected past a retracted record = %d, want 409", code)
	}
	if !confirmed(outside) || confirmed(later) {
		t.Errorf("confirm-selected: first record confirmed = %v, record after the refusal confirmed = %v", confirmed(outside), confirmed(later))
	}
}
