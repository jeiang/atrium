package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func runJob(t *testing.T, f *fixture, d *db.DB, name string) int64 {
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
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = ?", name).Scan(&jobID); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if jobID == 0 {
		t.Fatalf("the scheduler did not create the %s job", name)
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
		t.Fatalf("the %s job did not run", name)
	}
	return jobRun
}

func TestMorningDigestPostsToTodayAndPushes(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	browser.store(t, d)
	now := float64(time.Now().Unix())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.Write.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (1, 'reminder', 'pending', 100, 'Call the dentist', 'fp1')")
	exec("INSERT INTO commitments (id, message_id, text, owner, due, status, created_ts) VALUES (1, 1, 'Send invoice', 'me', ?, 'open', 100)", time.Unix(int64(now), 0).AddDate(0, 0, -1).Format("2006-01-02"))
	exec("INSERT INTO threads (id, kind, title, effort, model, context_limit, created_at, updated_at) VALUES (900, 'nightly', 'dreaming', 'high', 'claude-haiku-5-5', 90000, ?, ?)", now-3600, now-3600)
	exec("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at, ended_at) VALUES (900, 900, 'nightly', 'claude-haiku-5-5', 'high', 'done', ?, ?)", now-3600, now-3000)
	exec("INSERT INTO jobs (id, name, kind, prompt, schedule, tz, state, next_due, created_by, created_at, module, declaration_key, activated_at) VALUES (900, 'nightly', 'nightly', '', 'daily 02:30', 'UTC', 'active', NULL, 'test', 1, 'assistant', 'assistant/nightly', 1)")
	exec("INSERT INTO job_runs (id, job_id, due_at, started_at, ended_at, status, thread_id) VALUES (900, 900, ?, ?, ?, 'ok', 900)", now-3600, now-3600, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, source_run_id, source_thread_id, created_at, updated_at) VALUES (900, 'Tea', 'Prefers green tea.', 'personal', 'dreaming', 900, 1, ?, ?)", now-3000, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, source_run_id, source_thread_id, marks, created_at, updated_at) VALUES (901, 'Plumber', 'Friday.', 'personal', 'dreaming', 900, 1, 5, ?, ?)", now-3000, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, source_run_id, aliases, created_at, updated_at) VALUES (902, 'Lesson: check the source', 'x', 'personal', 'dreaming', 900, '[\"lesson\"]', ?, ?)", now-3000, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, source_run_id, source_thread_id, aliases, created_at, updated_at) VALUES (903, 'Conflict: Tea', 'newer fact', 'personal', 'dreaming', 900, 1, '[\"conflict\"]', ?, ?)", now-3000, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, source_run_id, private, created_at, updated_at) VALUES (904, 'Secret', 'hidden', 'personal', 'dreaming', 900, 1, ?, ?)", now-3000, now-3000)
	exec("INSERT INTO kb_records (id, title, body, scope, writer, created_at, updated_at) VALUES (905, 'Older', 'edited', 'personal', 'chat', ?, ?)", now-7200, now-7200)
	exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, run_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (901, ?, ?, 'digest-901', 'agent', 'kb_write', 'digest-901', 'forward', 900, 'kb:900', '{}', '{}', '{}', '{}', 'done', ?)", now-3000, now-3000, now-3000)
	exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, run_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (902, ?, ?, 'digest-902', 'agent', 'kb_write', 'digest-902', 'forward', 900, 'kb:905', '{}', '{}', '{}', '{}', 'done', ?)", now-3000, now-3000, now-3000)
	exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, run_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (903, ?, ?, 'digest-903', 'agent', 'kb_retract', 'digest-903', 'forward', 900, 'kb:905', '{}', '{}', '{}', '{}', 'done', ?)", now-3000, now-3000, now-3000)
	exec("INSERT INTO effects (id, created_at, lease_at, token, origin, operation, operation_key, direction, run_id, target, action_json, before_json, after_json, undo_json, state, settled_at) VALUES (904, ?, ?, 'digest-904', 'agent', 'task_add', 'digest-904', 'forward', 900, 'task:1', '{}', '{}', '{}', '{}', 'done', ?)", now-3000, now-3000, now-3000)
	requests := len(api.Requests())
	runJob(t, f, d, "daily-nudges")
	if len(api.Requests()) != requests {
		t.Errorf("the digest made a model call")
	}
	var threadID int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE role = 'today' ORDER BY id DESC LIMIT 1").Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND content_json LIKE '%\"type\":\"digest\"%' ORDER BY id DESC LIMIT 1", threadID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Call the dentist", "1 commitments", "Dreaming: 4 new, 1 edited, 1 retracted, 1 tasks", "Lessons added: 1", "K903 may be outdated", "1 records from marked sources"} {
		if !strings.Contains(content, want) {
			t.Errorf("digest %q lacks %q", content, want)
		}
	}
	for _, bad := range []string{"Send invoice", "Secret", "hidden"} {
		if strings.Contains(content, bad) {
			t.Errorf("digest shows %q", bad)
		}
	}
	body := browser.decrypt(t, push.wait(t, 1).body)
	if !strings.Contains(fmt.Sprint(body["body"]), "Send invoice") {
		t.Errorf("push body = %v, want the commitment title", body["body"])
	}
}

func TestDigestStaysQuietWhenNothingToAct(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	push, _ := pushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	before := push.count()
	runJob(t, f, d, "daily-nudges")
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE content_json LIKE '%\"type\":\"digest\"%'").Scan(&n); err != nil || n != 0 {
		t.Errorf("digest messages = %d, %v; want none", n, err)
	}
	if push.count() != before {
		t.Errorf("an empty digest pushed %d notifications", push.count()-before)
	}
}
