package main

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/unit"
)

func TestCommitmentResourceJoinsTasksAndMail(t *testing.T) {
	s := newStreamSite(t)
	frames := appFrames(t, s.openApp())
	synced := syncedRevisions(t, nextAppFrame(t, frames, "sync"))
	exec := func(query string, args ...any) { asstExec(t, s.d, query, args...) }
	today := time.Now().UTC()
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }
	exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1, 2)")
	exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES (1, 'icloud', 1, 1, 1, 'garden@test', 'garden', 'Garden plans', 'pat@example.com', 0)")
	exec(`INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, to_addrs, sent, first_seen) VALUES (2, 'icloud', 1, 1, 2, 'invoice@test', 'invoice', 'Invoice for the shed', 'me@example.com', '["sam@example.com"]', 1, 0)`)
	reminder, _, err := proposals.Create(testProposalContext(), s.d, "inbox_backlog", nil, "Remind me about the garden sketch", map[string]any{}, map[string]any{}, "Remind me about the garden sketch", 100)
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, proposal_id, created_ts) VALUES (1, 1, 'Send the garden sketch', 'me', ?, 'open', ?, 100)", day(-1), reminder)
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (2, 2, 'Sam sends the invoice', 'them', ?, 'open', 101)", day(4))
	exec("INSERT INTO commitments (id, message_id, text, owner, status, created_ts) VALUES (3, 1, 'Water the plants', 'me', 'done', 102)")

	call := func(method, path string, body any, status int) (map[string]any, int64) {
		t.Helper()
		got := s.do(method, path, s.token, body)
		data := apiData(t, got, status)
		effects, _ := got.json["effect_ids"].([]any)
		if len(effects) != 1 {
			t.Fatalf("%s %s: effect_ids %v", method, path, got.json["effect_ids"])
		}
		return data, number(t, effects[0])
	}
	undo := func(effect int64) {
		t.Helper()
		if got := s.do(http.MethodPost, fmt.Sprintf("/api/v1/undo/write:%d", effect), s.token, nil); got.status != http.StatusOK {
			t.Fatalf("undo %d: %d %s", effect, got.status, got.body)
		}
	}
	taskRef := func(title string, due any) string {
		t.Helper()
		spec := map[string]any{"title": title}
		if due != nil {
			spec["due"] = due
		}
		made, _ := call(http.MethodPost, "/api/v1/tasks", spec, http.StatusCreated)
		return made["task"].(map[string]any)["ref"].(string)
	}
	seeds := taskRef("Buy seeds", day(-2))
	fence := taskRef("Fix the fence", nil)
	synced["tasks"] = waitAppChange(t, frames, "tasks", synced["tasks"])

	get := func(ref string) map[string]any {
		t.Helper()
		return apiData(t, s.do(http.MethodGet, "/api/v1/commitments/"+ref, s.token, nil), http.StatusOK)["item"].(map[string]any)
	}
	list := func(query string) ([]string, map[string]any) {
		t.Helper()
		data := apiData(t, s.do(http.MethodGet, "/api/v1/commitments"+query, s.token, nil), http.StatusOK)
		var out []string
		for _, item := range apiItems(t, data) {
			out = append(out, item.(map[string]any)["title"].(string))
		}
		return out, data["counts"].(map[string]any)
	}
	order, counts := list("")
	if fmt.Sprint(order) != "[Buy seeds Send the garden sketch Fix the fence Water the plants Sam sends the invoice]" {
		t.Fatalf("order = %v", order)
	}
	if fmt.Sprint(counts) != "map[todo:map[dismissed:0 done:1 open:3] waiting:map[dismissed:0 done:0 open:1]]" {
		t.Fatalf("counts = %v", counts)
	}
	sketch := get("commitment:1")
	links, _ := sketch["links"].([]any)
	if sketch["side"] != "todo" || sketch["owner"] != "me" || sketch["counterpart"] != "pat@example.com" || sketch["source"] != "commitment" ||
		fmt.Sprint(sketch["actions"]) != "[done dismiss snooze]" || len(links) != 2 {
		t.Fatalf("commitment = %v", sketch)
	}
	if l := links[0].(map[string]any); l["kind"] != "message" || l["id"] != "1" || l["title"] != "Garden plans" {
		t.Fatalf("message link = %v", l)
	}
	if l := links[1].(map[string]any); l["kind"] != "proposal" || l["id"] != strconv.FormatInt(reminder, 10) || l["title"] == nil {
		t.Fatalf("proposal link = %v", l)
	}
	if invoice := get("commitment:2"); invoice["side"] != "waiting" || invoice["owner"] != "them" || invoice["counterpart"] != "sam@example.com" {
		t.Fatalf("waiting commitment = %v", invoice)
	}
	if task := get(seeds); task["source"] != "task" || task["owner"] != "me" || task["side"] != "todo" || !slices.Contains(task["actions"].([]any), any("edit")) {
		t.Fatalf("task item = %v", task)
	}
	for query, want := range map[string]string{
		"?side=waiting":                              "[Sam sends the invoice]",
		"?side=todo&status=done":                     "[Water the plants]",
		"?due_from=" + day(-1) + "&due_to=" + day(4): "[Send the garden sketch Sam sends the invoice]",
		"?q=GARDEN":                                  "[Send the garden sketch]",
		"?link=" + url.QueryEscape("message:1"):      "[Send the garden sketch Water the plants]",
	} {
		if got, _ := list(query); fmt.Sprint(got) != want {
			t.Fatalf("commitments%s = %v, want %s", query, got, want)
		}
	}
	if _, filtered := list("?side=waiting&status=dismissed"); fmt.Sprint(filtered) != fmt.Sprint(counts) {
		t.Fatalf("counts follow the filter: %v", filtered)
	}
	expectField(t, s.do(http.MethodGet, "/api/v1/commitments?due_from=soon", s.token, nil), "due_from")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commitments?side=later", s.token, nil), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commitments/parcel:1", s.token, nil), http.StatusUnprocessableEntity, "invalid")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commitments/commitment:999", s.token, nil), http.StatusNotFound, "not_found")

	first := apiData(t, s.do(http.MethodGet, "/api/v1/commitments?limit=2", s.token, nil), http.StatusOK)
	var paged []string
	for page := first; ; {
		for _, item := range apiItems(t, page) {
			paged = append(paged, item.(map[string]any)["title"].(string))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		page = apiData(t, s.do(http.MethodGet, "/api/v1/commitments?limit=2&cursor="+url.QueryEscape(next), s.token, nil), http.StatusOK)
	}
	if fmt.Sprint(paged) != fmt.Sprint(order) {
		t.Fatalf("paged = %v", paged)
	}

	queue := apiData(t, s.do(http.MethodGet, "/api/v1/queue", s.token, nil), http.StatusOK)
	var queued []string
	for _, item := range apiItems(t, queue) {
		queued = append(queued, item.(map[string]any)["id"].(string))
	}
	if !slices.Contains(queued, "commitment:1") || !slices.Contains(queued, seeds) {
		t.Fatalf("queue ids %v miss the commitment refs", queued)
	}

	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	for _, query := range []string{"/commitments?limit=3", "/commitments/commitment:1", "/commitments/" + seeds} {
		bearer := s.do(http.MethodGet, "/api/v1"+query, s.token, nil)
		if cookie := browser.do(http.MethodGet, "/web/v1"+query, nil); bearer.status != http.StatusOK || cookie.body != bearer.body {
			t.Fatalf("%s differs between the surfaces: %.300s / %.300s", query, bearer.body, cookie.body)
		}
	}
	plain := s.do(http.MethodGet, "/api/v1/commitments", s.token, nil)
	etag := plain.header.Get("ETag")
	if etag == "" {
		t.Fatalf("no ETag: %v", plain.header)
	}
	if again := s.do(http.MethodGet, "/api/v1/commitments", s.token, nil, "If-None-Match", etag); again.status != http.StatusNotModified {
		t.Fatalf("conditional list = %d", again.status)
	}

	status := func(id int) (string, int64) {
		t.Helper()
		var st string
		var proposal int64
		if err := s.d.Read.QueryRow("SELECT status, COALESCE(proposal_id, 0) FROM commitments WHERE id = ?", id).Scan(&st, &proposal); err != nil {
			t.Fatal(err)
		}
		return st, proposal
	}
	done, effect := call(http.MethodPost, "/api/v1/commitments/commitment:1/done", nil, http.StatusOK)
	if done["ref"] != "commitment:1" || done["status"] != "done" || done["item"].(map[string]any)["status"] != "done" {
		t.Fatalf("done = %v", done)
	}
	synced["mail"] = waitAppChange(t, frames, "mail", synced["mail"])
	var receipts int
	if err := s.d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE id = ? AND operation = 'commitment_status' AND target = 'commitment:1'", effect).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("commitment effect rows = %d (%v)", receipts, err)
	}
	undo(effect)
	if st, _ := status(1); st != "open" {
		t.Fatalf("undone done left %s", st)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/commitments/commitment:1/reopen", s.token, nil), http.StatusConflict, "conflict")
	call(http.MethodPost, "/api/v1/commitments/commitment:2/dismiss", nil, http.StatusOK)
	expectAPI(t, s.do(http.MethodPost, "/api/v1/commitments/commitment:2/dismiss", s.token, nil), http.StatusConflict, "conflict")
	if got := get("commitment:2"); fmt.Sprint(got["actions"]) != "[reopen]" {
		t.Fatalf("dismissed actions = %v", got["actions"])
	}
	_, effect = call(http.MethodPost, "/api/v1/commitments/commitment:2/reopen", nil, http.StatusOK)
	undo(effect)
	if st, _ := status(2); st != "dismissed" {
		t.Fatalf("undone reopen left %s", st)
	}
	call(http.MethodPost, "/api/v1/commitments/commitment:1/dismiss", nil, http.StatusOK)
	call(http.MethodPost, "/api/v1/commitments/commitment:1/reopen", nil, http.StatusOK)
	if st, proposal := status(1); st != "open" || proposal != reminder {
		t.Fatalf("reopened commitment = %s proposal %d, want open %d", st, proposal, reminder)
	}
	expectAPI(t, s.do(http.MethodPatch, "/api/v1/commitments/commitment:1", s.token, map[string]any{"title": "Draw it"}), http.StatusConflict, "read_only")
	expectAPI(t, s.do(http.MethodDelete, "/api/v1/commitments/commitment:1", s.token, nil), http.StatusConflict, "read_only")
	if got := get("commitment:1"); got["title"] != "Send the garden sketch" {
		t.Fatalf("refused edit changed the commitment: %v", got)
	}

	closed, effect := call(http.MethodPost, "/api/v1/commitments/"+seeds+"/done", nil, http.StatusOK)
	if closed["status"] != "done" {
		t.Fatalf("task done = %v", closed)
	}
	synced["tasks"] = waitAppChange(t, frames, "tasks", synced["tasks"])
	expectAPI(t, s.do(http.MethodPost, "/api/v1/commitments/"+seeds+"/done", s.token, nil), http.StatusConflict, "conflict")
	undo(effect)
	if got := get(seeds); got["status"] != "open" {
		t.Fatalf("undone task done = %v", got)
	}
	before := get(fence)
	edited, effect := call(http.MethodPatch, "/api/v1/commitments/"+fence, map[string]any{"title": "Fix the gate", "updated_ts": before["updated_ts"]}, http.StatusOK)
	if edited["item"].(map[string]any)["title"] != "Fix the gate" {
		t.Fatalf("task edit = %v", edited)
	}
	undo(effect)
	_, effect = call(http.MethodDelete, "/api/v1/commitments/"+fence, nil, http.StatusOK)
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commitments/"+fence, s.token, nil), http.StatusNotFound, "not_found")
	undo(effect)
	if got := get(fence); got["title"] != "Fix the fence" {
		t.Fatalf("restored task = %v", got)
	}
	if got := apiData(t, browser.do(http.MethodPost, "/web/v1/commitments/commitment:1/done", nil), http.StatusOK); got["status"] != "done" {
		t.Fatalf("web done = %v", got)
	}
	call(http.MethodPost, "/api/v1/commitments/commitment:1/reopen", nil, http.StatusOK)

	legacy := apiData(t, s.do(http.MethodGet, "/api/v1/tasks", s.token, nil), http.StatusOK)
	if items := apiItems(t, legacy); len(items) != 2 || items[0].(map[string]any)["source_kind"] == nil {
		t.Fatalf("/tasks changed: %v", legacy)
	}
	apiData(t, s.do(http.MethodGet, "/api/v1/mail/commitments", s.token, nil), http.StatusOK)
	mailOn, _ := list("")

	seedSettings(t, map[string]string{module.FlagKey(unit.Mail): "false"})
	s.restart()
	if got, _ := list(""); fmt.Sprint(got) != fmt.Sprint(mailOn) {
		t.Fatalf("mail off = %v, want the stored commitments still listed: %v", got, mailOn)
	}
	apiData(t, s.do(http.MethodGet, "/api/v1/commitments/commitment:1", s.token, nil), http.StatusOK)
	apiData(t, s.do(http.MethodGet, "/api/v1/mail/commitments", s.token, nil), http.StatusOK)
	seedSettings(t, map[string]string{module.FlagKey(unit.Mail): "true", module.FlagKey(unit.Assistant): "false"})
	s.restart()
	if got, _ := list(""); fmt.Sprint(got) != "[Send the garden sketch Water the plants Sam sends the invoice]" {
		t.Fatalf("assistant off = %v", got)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/commitments/"+seeds, s.token, nil), http.StatusNotFound, "not_found")
}

func TestCommitmentToolsMergeTaskAndMailLists(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := mustConnect(t, dir+"/atrium.db", keyFor(t))
	defer func() { _ = d.Close() }()
	exec := func(query string, args ...any) { asstExec(t, d, query, args...) }
	exec("INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1, 1)")
	exec("INSERT INTO messages (id, account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES (1, 'icloud', 1, 1, 1, 'quote@test', 'quote', 'Roof quote', 'roofer@example.com', 0)")
	exec("INSERT INTO commitments (id, message_id, text, owner, status, created_ts) VALUES (1, 1, 'Roofer sends the quote', 'them', 'open', 100)")
	exec("INSERT INTO tasks (title, status, created_ts, updated_ts) VALUES ('Clear the gutters', 'open', 100, 100)")
	var task int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Clear the gutters'").Scan(&task); err != nil {
		t.Fatal(err)
	}
	ref := "task:" + strconv.FormatInt(task, 10)
	results := func() string {
		t.Helper()
		var out string
		if err := d.Read.QueryRow("SELECT group_concat(content_json) FROM thread_messages WHERE thread_id = (SELECT MAX(id) FROM threads) AND role = 'user'").Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	run := func(kind, id, name string, args map[string]any) appRun {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse(id, name, args)))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
		ran := runApp(t, "agent", "run", "--kind", kind, "--text", "check my list")
		if ran.code != cli.ExitOK {
			t.Fatalf("%s run: %d %s", kind, ran.code, ran.stderr)
		}
		return ran
	}

	run("chat", "l1", "commitment_list", map[string]any{})
	if got := results(); !strings.Contains(got, "commitment:1") || !strings.Contains(got, ref) || !strings.Contains(got, "roofer@example.com") {
		t.Fatalf("chat commitment_list: %s", got)
	}
	run("chat", "l2", "commitment_list", map[string]any{"side": "waiting"})
	if got := results(); !strings.Contains(got, "Roofer sends the quote") || strings.Contains(got, "Clear the gutters") {
		t.Fatalf("waiting side: %s", got)
	}
	nightly := run("nightly", "l3", "commitment_list", map[string]any{})
	if got := results(); strings.Contains(got, "commitment:1") || !strings.Contains(got, ref) {
		t.Fatalf("nightly commitment_list: %s", got)
	}
	names := offeredTools(t, api, nightly)
	for _, gone := range []string{"task_list", "task_done", "commitments"} {
		if containsName(names, gone) {
			t.Fatalf("retired tool %s still offered: %v", gone, names)
		}
	}
	if containsName(names, "task_close") || containsName(names, "task_update") {
		t.Fatalf("nightly run offered a chat-only write: %v", names)
	}

	run("chat", "c1", "task_close", map[string]any{"ref": "commitment:1"})
	var st string
	if err := d.Read.QueryRow("SELECT status FROM commitments WHERE id = 1").Scan(&st); err != nil || st != "open" {
		t.Fatalf("task_close changed a commitment: %s %v", st, err)
	}
	if got := results(); !strings.Contains(got, "is_error") {
		t.Fatalf("commitment ref was not refused: %s", got)
	}
	run("chat", "u1", "task_update", map[string]any{"ref": "commitment:1", "title": "Quote"})
	if got := results(); !strings.Contains(got, "is_error") {
		t.Fatalf("task_update on a commitment was not refused: %s", got)
	}
	for _, kind := range []string{"cron", "nightly", "sub"} {
		ran := run(kind, "c2"+kind, "task_close", map[string]any{"ref": ref})
		if err := d.Read.QueryRow("SELECT status FROM tasks WHERE id = ?", task).Scan(&st); err != nil || st != "open" {
			t.Fatalf("%s closed a task: %s %v", kind, st, err)
		}
		if names := offeredTools(t, api, ran); containsName(names, "task_close") {
			t.Fatalf("%s run offered task_close: %v", kind, names)
		}
	}
	run("chat", "c3", "task_close", map[string]any{"ref": ref, "outcome": "dismissed"})
	if err := d.Read.QueryRow("SELECT status FROM tasks WHERE id = ?", task).Scan(&st); err != nil || st != "dismissed" {
		t.Fatalf("task_close dismissed = %s %v", st, err)
	}
	var audits int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'tasks.dismiss' AND target = ? AND outcome = 'ok' AND source = 'tool'", strconv.FormatInt(task, 10)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("dismiss audit rows = %d (%v)", audits, err)
	}
}
