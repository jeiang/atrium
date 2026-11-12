package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

func dreamingRun(t *testing.T, f *fixture, d *db.DB) int64 {
	t.Helper()
	start := func() (context.CancelFunc, chan appRun) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan appRun, 1)
		go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
		return cancel, done
	}
	cancel, done := start()
	var jobID int64
	for range 100 {
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'nightly'").Scan(&jobID); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if jobID == 0 {
		t.Fatalf("the scheduler did not create the nightly job")
	}
	if _, err := d.Write.Exec("UPDATE jobs SET next_due = NULL WHERE id != ?", jobID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE job_id = ? AND ended_at IS NOT NULL", jobID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE id = ?",
		float64(time.Now().Unix())-1, jobID); err != nil {
		t.Fatal(err)
	}
	cancel, done = start()
	var jobRun int64
	for range 400 {
		var now int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs WHERE job_id = ? AND ended_at IS NOT NULL", jobID).Scan(&now); err == nil && now > before {
			if err := d.Read.QueryRow("SELECT id FROM job_runs WHERE job_id = ? AND ended_at IS NOT NULL ORDER BY id DESC LIMIT 1", jobID).Scan(&jobRun); err == nil {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if jobRun == 0 {
		t.Fatalf("the nightly job did not run")
	}
	return jobRun
}

func chatThread(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE kind = 'chat' ORDER BY id DESC LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func requestText(t *testing.T, r anthropictest.Request) string {
	t.Helper()
	var b strings.Builder
	messages, _ := r.JSON(t)["messages"].([]any)
	for _, raw := range messages {
		m, _ := raw.(map[string]any)
		content, _ := m["content"].([]any)
		for _, entry := range content {
			block, _ := entry.(map[string]any)
			if text, ok := block["text"].(string); ok {
				b.WriteString(text)
			}
		}
	}
	return b.String()
}

func TestDreamingWritesFromACleanThreadUnmarked(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	source := chatThread(t, d)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Tea", "body": "Prefers green tea.", "source": map[string]any{"thread_id": source},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("wrote Tea")))
	requests := len(api.Requests())
	jobRun := dreamingRun(t, f, d)
	if len(api.Requests()) != requests+2 {
		t.Fatalf("%d model requests, want 2 for one dreaming run", len(api.Requests())-requests)
	}
	tools := requestTools(t, api.Requests()[requests])
	for _, name := range []string{"kb_write", "kb_retract", "kb_search", "task_add"} {
		if !containsName(tools, name) {
			t.Errorf("dreaming run lacks %s: %v", name, tools)
		}
	}
	for _, name := range []string{"mail_read", "web_fetch", "notify", "search_web"} {
		if containsName(tools, name) {
			t.Errorf("dreaming run was offered %s: %v", name, tools)
		}
	}
	if text := requestText(t, api.Requests()[requests]); !strings.Contains(text, "I prefer green tea") || !strings.Contains(text, fmt.Sprintf("thread %d", source)) {
		t.Errorf("gather text = %q, want the chat text and its source id", text)
	}
	var id, marks, sourceThread, sourceRun int64
	var writer string
	var confirmed *float64
	if err := d.Read.QueryRow("SELECT id, writer, marks, source_thread_id, source_run_id, confirmed_at FROM kb_records WHERE title = 'Tea'").
		Scan(&id, &writer, &marks, &sourceThread, &sourceRun, &confirmed); err != nil {
		t.Fatal(err)
	}
	if writer != "dreaming" || marks != 0 || sourceThread != source || confirmed != nil {
		t.Errorf("record writer=%s marks=%d source=%d confirmed=%v, want dreaming, 0, %d, unconfirmed", writer, marks, sourceThread, confirmed, source)
	}
	var runThread int64
	if err := d.Read.QueryRow("SELECT thread_id FROM job_runs WHERE id = ?", jobRun).Scan(&runThread); err != nil || runThread == 0 {
		t.Errorf("job run thread = %d, %v; want the dreaming thread", runThread, err)
	}
	var runSource int64
	if err := d.Read.QueryRow("SELECT source_run_id FROM kb_records WHERE id = ?", id).Scan(&runSource); err != nil || runSource == 0 {
		t.Errorf("source_run_id = %d, %v; want the write run", runSource, err)
	}
}

func TestDreamingMailFactsWaitForConfirm(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "the plumber is coming on Friday"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	source := chatThread(t, d)
	if _, err := d.Write.Exec("UPDATE threads SET marks = ?, tainted_at = ? WHERE id = ?", 5, float64(time.Now().Unix()), source); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Plumber", "body": "The plumber comes on Friday.", "source": map[string]any{"thread_id": source},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("wrote Plumber")))
	dreamingRun(t, f, d)
	var marks int64
	var confirmed *float64
	if err := d.Read.QueryRow("SELECT marks, confirmed_at FROM kb_records WHERE title = 'Plumber'").Scan(&marks, &confirmed); err != nil {
		t.Fatal(err)
	}
	if marks != 5 || confirmed != nil {
		t.Errorf("mail record marks=%d confirmed=%v, want 5 and unconfirmed", marks, confirmed)
	}
	var threadMarks int64
	if err := d.Read.QueryRow("SELECT marks FROM threads WHERE id = (SELECT MAX(thread_id) FROM job_runs)").Scan(&threadMarks); err != nil || threadMarks != 5 {
		t.Errorf("dreaming thread marks = %d, %v; want the gathered marks", threadMarks, err)
	}
}

func TestDreamingRefusesUnreadSourcesAndConfirmedEdits(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	source := chatThread(t, d)
	if _, err := d.Write.Exec("INSERT INTO kb_records (title, body, scope, writer, confirmed_at, created_at, updated_at) VALUES ('Pinned fact', 'old', 'personal', 'operator', 1, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	var confirmed int64
	if err := d.Read.QueryRow("SELECT id FROM kb_records WHERE title = 'Pinned fact'").Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Bad source", "body": "x", "source": map[string]any{"thread_id": 999999},
	})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w2", "kb_write", map[string]any{
		"id": confirmed, "body": "changed", "source": map[string]any{"thread_id": source},
	})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w3", "kb_write", map[string]any{
		"title": "Tea", "body": "Prefers green tea.", "source": map[string]any{"thread_id": source},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+4 {
		t.Fatalf("%d requests, want 4", len(all)-requests)
	}
	first := toolResultFor(t, all[requests+1], "w1")
	if first["is_error"] != true || !strings.Contains(fmt.Sprint(first["content"]), "gathered") {
		t.Errorf("unread source result = %v, want a refusal", first)
	}
	second := toolResultFor(t, all[requests+2], "w2")
	if second["is_error"] != true || !strings.Contains(fmt.Sprint(second["content"]), "confirmed") {
		t.Errorf("confirmed edit result = %v, want a refusal", second)
	}
	var body string
	if err := d.Read.QueryRow("SELECT body FROM kb_records WHERE title = 'Pinned fact'").Scan(&body); err != nil || body != "old" {
		t.Errorf("confirmed record body = %q, %v; want unchanged", body, err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM kb_records WHERE title = 'Tea'").Scan(&n); err != nil || n != 1 {
		t.Errorf("valid record count = %d, %v; want 1", n, err)
	}
}

func TestDreamingStopsAtItsCaps(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "I prefer green tea"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	set := func(key, value string) {
		t.Helper()
		if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
			t.Fatal(err)
		}
	}
	set(assistant.KeyDreamingMaxCost, "0.000001")
	requests := len(api.Requests())
	jobRun := dreamingRun(t, f, d)
	var status string
	if err := d.Read.QueryRow("SELECT status FROM job_runs WHERE id = ?", jobRun).Scan(&status); err != nil || status != "skipped_budget" {
		t.Errorf("job status = %q, %v; want skipped_budget", status, err)
	}
	if len(api.Requests()) != requests {
		t.Errorf("the capped run made %d model requests", len(api.Requests())-requests)
	}

	set(assistant.KeyDreamingMaxCost, "0.30")
	set(assistant.KeyDreamingMonthly, "0.000001")
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, model, context_limit, created_at, updated_at) VALUES ('nightly', 'old dreaming', 'high', 'claude-haiku-5-5', 90000, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO usage (attempt_key, ts, month, expires_at, settled_at, state, budget_class, reservation_nusd, accounted_nusd, request_id, source, provider, thread_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tier, cost_usd) "+
		"SELECT lower(hex(randomblob(16))), ?, ?, ?, ?, 'settled', 'unattended', 0, 10000000, 'r1', 'assistant', 'anthropic', id, 'nightly', 'claude-haiku-5-5', 'high', '[]', 0,0,0,0,0,0,0,'standard', 0.01 FROM threads WHERE kind = 'nightly' LIMIT 1",
		now, asstMonth(t, now), now, now); err != nil {
		t.Fatal(err)
	}
	requests = len(api.Requests())
	jobRun = dreamingRun(t, f, d)
	if err := d.Read.QueryRow("SELECT status FROM job_runs WHERE id = ?", jobRun).Scan(&status); err != nil || status != "skipped_budget" {
		t.Errorf("monthly capped job status = %q, %v; want skipped_budget", status, err)
	}
	if len(api.Requests()) != requests {
		t.Errorf("the monthly capped run made %d model requests", len(api.Requests())-requests)
	}
}

func TestDreamingSkipsPrivateProjectThreads(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "public fact"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	if _, err := d.Write.Exec("INSERT INTO projects (name, instructions, effort, context_limit, private, archived, created_at, updated_at) VALUES ('Secret', '', 'low', 90000, 1, 0, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	var project int64
	if err := d.Read.QueryRow("SELECT id FROM projects WHERE name = 'Secret'").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, project_id, effort, model, context_limit, created_at, updated_at) VALUES ('chat', 'private', ?, 'low', 'claude-haiku-5-5', 90000, 1, 1)", project); err != nil {
		t.Fatal(err)
	}
	var privateThread int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE title = 'private'").Scan(&privateThread); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', ?, ?)",
		privateThread, `[{"type":"text","text":"the secret is 4417"}]`, float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("nothing")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	all := api.Requests()
	if len(all) != requests+1 {
		t.Fatalf("%d requests, want 1", len(all)-requests)
	}
	if text := requestText(t, all[requests]); strings.Contains(text, "4417") {
		t.Errorf("gather text carries a private-project message: %q", text)
	}
}

func TestDreamingMonthlyCapRaisesAttention(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "hi"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	if _, err := d.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", assistant.KeyDreamingMonthly, "0.000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, model, context_limit, created_at, updated_at) VALUES ('nightly', 'old dreaming', 'high', 'claude-haiku-5-5', 90000, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	if _, err := d.Write.Exec("INSERT INTO usage (attempt_key, ts, month, expires_at, settled_at, state, budget_class, reservation_nusd, accounted_nusd, request_id, source, provider, thread_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, reasoning_tokens, searches, tier, cost_usd) "+
		"SELECT lower(hex(randomblob(16))), ?, ?, ?, ?, 'settled', 'unattended', 0, 10000000, 'r1', 'assistant', 'anthropic', id, 'nightly', 'claude-haiku-5-5', 'high', '[]', 0,0,0,0,0,0,0,'standard', 0.01 FROM threads WHERE kind = 'nightly' LIMIT 1",
		now, asstMonth(t, now), now, now); err != nil {
		t.Fatal(err)
	}
	ctx := config.With(settings.With(context.Background(), f.settings), f.state)
	items, err := dreaming.Attention(f.lookup).Items(ctx, d.Read, float64(time.Now().Unix()))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "dreaming_budget" || items[0].Group != attention.NeedsDecision {
		t.Fatalf("attention items = %+v, want the monthly cap item", items)
	}
}

func TestUndoneWriteBecomesALesson(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db", db.Key{})
	source := chatThread(t, d)
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, thread_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (9301, ?, ?, 'dream-9301', 'agent', 'task_add', 'dream-9301', 'forward', ?, 'task:1', '{}', '{}', '{}', '{}', 'done', ?)", float64(time.Now().Unix())-60, float64(time.Now().Unix())-60, source, float64(time.Now().Unix())-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, thread_id, target, action_json, state, settled_at) VALUES (9302, ?, ?, 'dream-9302', 'agent', 'task_delete', 'dream-9301', 'undo', 9301, ?, 'task:1', '{}', 'done', ?)", float64(time.Now().Unix())-60, float64(time.Now().Unix())-60, source, float64(time.Now().Unix())-60); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE effects SET undone_by = 9302 WHERE id = 9301"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "kb_write", map[string]any{
		"title": "Lesson: confirm the task before adding", "body": "The operator undid a task_add.",
		"aliases": []any{"task_add"}, "source": map[string]any{"thread_id": source},
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("wrote the lesson")))
	requests := len(api.Requests())
	dreamingRun(t, f, d)
	if text := requestText(t, api.Requests()[requests]); !strings.Contains(text, "operator undid task_add") {
		t.Errorf("gather text lacks the undone write signal: %q", text)
	}
	var title string
	if err := d.Read.QueryRow("SELECT title FROM kb_records WHERE title LIKE 'Lesson: %'").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Lesson: confirm the task before adding" {
		t.Errorf("lesson title = %q", title)
	}
}
