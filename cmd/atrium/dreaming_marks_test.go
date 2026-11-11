package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestDreamingSplitsMarkedAndUnmarkedThreads(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	unmarked := chatThread(t, d)
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, model, context_limit, marks, tainted_at, created_at, updated_at) VALUES ('chat', 'marked chat', 'low', 'claude-haiku-5-5', 90000, 5, ?, ?, ?)", now, now, now); err != nil {
		t.Fatal(err)
	}
	var marked int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE title = 'marked chat'").Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', ?, ?)",
		marked, `[{"type":"text","text":"the plumber comes on Friday"}]`, now); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Tea", "body": "Prefers green tea.", "source": map[string]any{"thread_id": unmarked},
	})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w2", "kb_write", map[string]any{
		"title": "Laundered", "body": "from the marked thread", "source": map[string]any{"thread_id": marked},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("unmarked done")))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w3", "kb_write", map[string]any{
		"title": "Plumber", "body": "The plumber comes on Friday.", "source": map[string]any{"thread_id": marked},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("marked done")))
	requests := len(api.Requests())
	jobRun := dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+5 {
		t.Fatalf("%d model requests, want 5 for two dreaming runs", len(all)-requests)
	}
	first := requestText(t, all[requests])
	if !strings.Contains(first, "I prefer green tea") || !strings.Contains(first, fmt.Sprintf("thread %d", unmarked)) {
		t.Errorf("unmarked run text lacks its thread: %q", first)
	}
	for _, bad := range []string{"plumber", fmt.Sprintf("thread %d", marked)} {
		if strings.Contains(first, bad) {
			t.Errorf("unmarked run text carries marked content %q", bad)
		}
	}
	second := requestText(t, all[requests+3])
	if !strings.Contains(second, "plumber") || !strings.Contains(second, fmt.Sprintf("thread %d", marked)) {
		t.Errorf("marked run text lacks its thread: %q", second)
	}
	if strings.Contains(second, "green tea") || strings.Contains(second, fmt.Sprintf("thread %d", unmarked)) {
		t.Errorf("marked run text carries unmarked content: %q", second)
	}
	if result := toolResultFor(t, all[requests+2], "w2"); result["is_error"] != true || !strings.Contains(fmt.Sprint(result["content"]), "gathered") {
		t.Errorf("laundered source result = %v, want a gathered refusal", result)
	}
	var marks int64
	if err := d.Read.QueryRow("SELECT marks FROM kb_records WHERE title = 'Tea'").Scan(&marks); err != nil || marks != 0 {
		t.Errorf("Tea marks = %d, %v; want 0", marks, err)
	}
	if err := d.Read.QueryRow("SELECT marks FROM kb_records WHERE title = 'Plumber'").Scan(&marks); err != nil || marks != 5 {
		t.Errorf("Plumber marks = %d, %v; want 5", marks, err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE title = 'Laundered'").Scan(&n); err != nil || n != 0 {
		t.Errorf("laundered records = %d, %v; want 0", n, err)
	}
	type nightlyThread struct {
		id    int64
		title string
		marks int64
	}
	rows, err := d.Read.Query("SELECT id, title, marks FROM threads WHERE kind = 'nightly' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var nightly []nightlyThread
	for rows.Next() {
		var thread nightlyThread
		if err := rows.Scan(&thread.id, &thread.title, &thread.marks); err != nil {
			t.Fatal(err)
		}
		nightly = append(nightly, thread)
	}
	if len(nightly) != 2 || nightly[0].marks != 0 || nightly[1].marks != 5 {
		t.Fatalf("nightly threads = %+v, want marks 0 then 5", nightly)
	}
	if !strings.Contains(nightly[1].title, "marked") {
		t.Errorf("marked thread title = %q", nightly[1].title)
	}
	var stored int64
	if err := d.Read.QueryRow("SELECT thread_id FROM job_runs WHERE id = ?", jobRun).Scan(&stored); err != nil || stored != nightly[0].id {
		t.Errorf("job run thread = %d, %v; want the unmarked thread %d", stored, err, nightly[0].id)
	}
}

func TestDreamingSignalsFollowTheirThreadMarks(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, model, context_limit, marks, tainted_at, created_at, updated_at) VALUES ('chat', 'signal origin', 'low', 'claude-haiku-5-5', 90000, 5, ?, ?, ?)", now, now, now); err != nil {
		t.Fatal(err)
	}
	var marked int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE title = 'signal origin'").Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, error, started_at, ended_at) VALUES (?, 'chat', 'claude-haiku-5-5', 'low', 'failed', 'secret page 4417', ?, ?)", marked, now-60, now-30); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, thread_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (9101, ?, ?, 'dream-9101', 'agent', 'task_add', 'dream-9101', 'forward', ?, 'task:secret', '{}', '{}', '{}', '{}', 'done', ?)", now-60, now-60, marked, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, thread_id, target, action_json, state, settled_at) VALUES (9102, ?, ?, 'dream-9102', 'agent', 'task_delete', 'dream-9101', 'undo', 9101, ?, 'task:secret', '{}', 'done', ?)", now-60, now-60, marked, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE effects SET undone_by = 9102 WHERE id = 9101"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO proposals (kind, status, created_ts, decided_ts, title, payload, fingerprint) VALUES ('permission', 'dismissed', ?, ?, 'dismissed secret 4417', ?, 'fp-secret')", now-60, now-30, fmt.Sprintf(`{"thread_id": %d}`, marked)); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Laundered", "body": "from the signal thread", "source": map[string]any{"thread_id": marked},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("unmarked done")))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w2", "kb_write", map[string]any{
		"title": "Lesson: check the secret page", "body": "The operator undid task:secret.",
		"aliases": []any{"task_add"}, "source": map[string]any{"thread_id": marked},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("marked done")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+4 {
		t.Fatalf("%d model requests, want 4 for two dreaming runs", len(all)-requests)
	}
	first := requestText(t, all[requests])
	for _, bad := range []string{"4417", "task:secret", "dismissed secret", fmt.Sprintf("thread %d", marked)} {
		if strings.Contains(first, bad) {
			t.Errorf("unmarked run text carries a marked signal %q", bad)
		}
	}
	second := requestText(t, all[requests+2])
	for _, want := range []string{"failed chat run in thread " + fmt.Sprint(marked), "operator undid task_add on task:secret", "operator dismissed permission proposal", fmt.Sprintf("thread %d", marked)} {
		if !strings.Contains(second, want) {
			t.Errorf("marked run text lacks %q: %q", want, second)
		}
	}
	if result := toolResultFor(t, all[requests+1], "w1"); result["is_error"] != true || !strings.Contains(fmt.Sprint(result["content"]), "gathered") {
		t.Errorf("signal-only source result = %v, want a gathered refusal", result)
	}
	var marks, source int64
	if err := d.Read.QueryRow("SELECT marks, source_thread_id FROM kb_records WHERE title = 'Lesson: check the secret page'").Scan(&marks, &source); err != nil {
		t.Fatal(err)
	}
	if marks != 5 || source != marked {
		t.Errorf("lesson marks=%d source=%d, want 5 and %d", marks, source, marked)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE title = 'Laundered'").Scan(&n); err != nil || n != 0 {
		t.Errorf("laundered records = %d, %v; want 0", n, err)
	}
}

func TestDreamingSignalsSkipPrivateProjects(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "public fact"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO projects (name, instructions, effort, context_limit, private, archived, created_at, updated_at) VALUES ('Secret', '', 'low', 90000, 1, 0, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	var project int64
	if err := d.Read.QueryRow("SELECT id FROM projects WHERE name = 'Secret'").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, project_id, effort, model, context_limit, marks, tainted_at, created_at, updated_at) VALUES ('chat', 'private signal', ?, 'low', 'claude-haiku-5-5', 90000, 5, ?, ?, ?)", project, now, now, now); err != nil {
		t.Fatal(err)
	}
	var private int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE title = 'private signal'").Scan(&private); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', ?, ?)",
		private, `[{"type":"text","text":"private message 8899"}]`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, error, started_at, ended_at) VALUES (?, 'chat', 'claude-haiku-5-5', 'low', 'failed', 'private error 8899', ?, ?)", private, now-60, now-30); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, thread_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (9201, ?, ?, 'dream-9201', 'agent', 'task_add', 'dream-9201', 'forward', ?, 'task:private-8899', '{}', '{}', '{}', '{}', 'done', ?)", now-60, now-60, private, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, thread_id, target, action_json, state, settled_at) VALUES (9202, ?, ?, 'dream-9202', 'agent', 'task_delete', 'dream-9201', 'undo', 9201, ?, 'task:private-8899', '{}', 'done', ?)", now-60, now-60, private, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE effects SET undone_by = 9202 WHERE id = 9201"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO proposals (kind, status, created_ts, decided_ts, title, payload, fingerprint) VALUES ('permission', 'dismissed', ?, ?, 'private proposal 8899', ?, 'fp-private')", now-60, now-30, fmt.Sprintf(`{"thread_id": %d}`, private)); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Laundered", "body": "from a private thread", "source": map[string]any{"thread_id": private},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+2 {
		t.Fatalf("%d model requests, want 2 for one dreaming run", len(all)-requests)
	}
	text := requestText(t, all[requests])
	for _, bad := range []string{"8899", "private message", "private error", "task:private", fmt.Sprintf("thread %d", private)} {
		if strings.Contains(text, bad) {
			t.Errorf("gather text carries private-project content %q", bad)
		}
	}
	if result := toolResultFor(t, all[requests+1], "w1"); result["is_error"] != true || !strings.Contains(fmt.Sprint(result["content"]), "gathered") {
		t.Errorf("private source result = %v, want a gathered refusal", result)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE title = 'Laundered'").Scan(&n); err != nil || n != 0 {
		t.Errorf("laundered records = %d, %v; want 0", n, err)
	}
	var nightly int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM threads WHERE kind = 'nightly'").Scan(&nightly); err != nil || nightly != 1 {
		t.Errorf("nightly threads = %d, %v; want 1", nightly, err)
	}
}

func TestDreamingRecordIndexFollowsVisibility(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, model, context_limit, marks, tainted_at, created_at, updated_at) VALUES ('chat', 'marked chat', 'low', 'claude-haiku-5-5', 90000, 5, ?, ?, ?)", now, now, now); err != nil {
		t.Fatal(err)
	}
	var marked int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE title = 'marked chat'").Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', ?, ?)",
		marked, `[{"type":"text","text":"the plumber comes on Friday"}]`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO projects (name, instructions, effort, context_limit, private, archived, created_at, updated_at) VALUES ('Secret', '', 'low', 90000, 1, 0, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	var project int64
	if err := d.Read.QueryRow("SELECT id FROM projects WHERE name = 'Secret'").Scan(&project); err != nil {
		t.Fatal(err)
	}
	record := func(title, scope string, marks, private int64, projectID any, extra string, args ...any) {
		t.Helper()
		query := "INSERT INTO kb_records (title, body, scope, writer, marks, private, project_id, created_at, updated_at" + extra + ") VALUES (?, 'x', ?, 'operator', ?, ?, ?, ?, ?" + strings.Repeat(", ?", len(args)) + ")"
		values := append([]any{title, scope, marks, private, projectID, now, now}, args...)
		if _, err := d.Write.Exec(query, values...); err != nil {
			t.Fatal(err)
		}
	}
	record("Visible fact", "personal", 0, 0, nil, "")
	record("Marked fact", "personal", 5, 0, nil, "")
	record("PII fact", "personal", 2, 0, nil, "")
	record("Private fact", "personal", 0, 1, nil, "")
	record("Project private fact", "personal", 0, 0, project, "")
	record("Expired fact", "personal", 0, 0, nil, ", expires_at", now-100)
	record("Retracted fact", "personal", 0, 0, nil, ", retracted_at", now-100)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("unmarked done")))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("marked done")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+2 {
		t.Fatalf("%d model requests, want 2 for two dreaming runs", len(all)-requests)
	}
	unmarked := requestText(t, all[requests])
	if !strings.Contains(unmarked, "Visible fact") {
		t.Errorf("unmarked index lacks the visible record: %q", unmarked)
	}
	for _, bad := range []string{"Marked fact", "PII fact", "Private fact", "Project private fact", "Expired fact", "Retracted fact"} {
		if strings.Contains(unmarked, bad) {
			t.Errorf("unmarked index lists %q", bad)
		}
	}
	markedText := requestText(t, all[requests+1])
	for _, want := range []string{"Visible fact", "Marked fact"} {
		if !strings.Contains(markedText, want) {
			t.Errorf("marked index lacks %q: %q", want, markedText)
		}
	}
	for _, bad := range []string{"PII fact", "Private fact", "Project private fact", "Expired fact", "Retracted fact"} {
		if strings.Contains(markedText, bad) {
			t.Errorf("marked index lists %q", bad)
		}
	}
}
