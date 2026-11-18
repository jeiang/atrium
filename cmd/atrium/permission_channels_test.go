package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

func permissionProposal(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'permission' ORDER BY id DESC LIMIT 1").
		Scan(&id); err != nil {
		t.Fatalf("permission proposal: %v", err)
	}
	return id
}

func waitRunState(t *testing.T, d *db.DB, thread int64, want string) {
	t.Helper()
	var state string
	for deadline := time.Now().Add(20 * time.Second); ; {
		err := d.Read.QueryRow("SELECT state FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&state)
		if err == nil && state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run state = %q, %v, want %q", state, err, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitParkedPermission(t *testing.T, d *db.DB, thread int64) int64 {
	t.Helper()
	var state string
	for deadline := time.Now().Add(20 * time.Second); ; {
		var id int64
		err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'permission' ORDER BY id DESC LIMIT 1").
			Scan(&id)
		_ = d.Read.QueryRow("SELECT state FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&state)
		if err == nil && state == "parked" {
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("no parked permission request: %v state %q", err, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeniedFetchResumesWithARefusal(t *testing.T) {
	t.Parallel()
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	start := len(api.Requests())
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/b"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("denied")))
	text := "read " + srv.URL + "/a then " + srv.URL + "/b"
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": text}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	proposal := waitParkedPermission(t, d, thread)
	if hits := pages.seen(); len(hits) != 1 || hits[0] != "/a" {
		t.Fatalf("page fetches before the decision = %v, want /a only", hits)
	}
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/deny", proposal), nil))
	waitRunState(t, d, thread, "done")
	if hits := pages.seen(); len(hits) != 1 || hits[0] != "/a" {
		t.Fatalf("page fetches after the denial = %v, want /a only", hits)
	}
	requests := api.Requests()[start:]
	refusal := toolResultFor(t, requests[len(requests)-1], "f2")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "the operator denied this request") {
		t.Errorf("denied fetch returned %v, want an error result", refusal)
	}
}

func TestDisabledAssistantPermissionDismissalDoesNotResume(t *testing.T) {
	t.Parallel()
	srv, pages := webPages(t)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, stop := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "web_fetch", map[string]any{"url": srv.URL + "/b"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("denied")))
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "read " + srv.URL + "/a then " + srv.URL + "/b"}))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	proposal := waitParkedPermission(t, d, thread)
	var runID int64
	var owner string
	if err := d.Read.QueryRow("SELECT id, COALESCE(owner, '') FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&runID, &owner); err != nil {
		t.Fatal(err)
	}
	stop()
	asstSet(t, "assistant_enabled", "false")
	base, _, stop = startServeApp(t, freshApp(f), &lockedBuffer{})
	client = webLocalG2(t, base)
	start := len(api.Requests())
	if got := client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/approve", proposal), nil); !strings.Contains(got.body, "the assistant module was disabled; start the action again") {
		t.Fatalf("disabled approval = %d: %s", got.status, got.body)
	}
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/deny", proposal), nil))
	var state, afterOwner, status string
	if err := d.Read.QueryRow("SELECT state, COALESCE(owner, '') FROM runs WHERE id = ?", runID).Scan(&state, &afterOwner); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", proposal).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if state != "parked" || afterOwner != owner || status != "dismissed" {
		t.Fatalf("run state = %q, owner = %q (was %q), proposal = %q", state, afterOwner, owner, status)
	}
	if got := len(api.Requests()); got != start {
		t.Fatalf("model requests after disabled decisions = %d, want %d", got, start)
	}
	if hits := pages.seen(); len(hits) != 1 || hits[0] != "/a" {
		t.Fatalf("page fetches = %v, want /a only", hits)
	}
	stop()
	asstSet(t, "assistant_enabled", "true")
	base, _, _ = startServeApp(t, freshApp(f), &lockedBuffer{})
	client = webLocalG2(t, base)
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/runs/%d/resume", thread, runID), nil))
	waitRunState(t, d, thread, "done")
	requests := api.Requests()
	if len(requests) <= start {
		t.Fatal("resume did not make a model request")
	}
	refusal := toolResultFor(t, requests[len(requests)-1], "f2")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "the operator denied this request") {
		t.Errorf("denied fetch returned %v, want an error result", refusal)
	}
}

func TestContactIdentifiersAskBeforeTheInternetButKeepTasksAndNotify(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	contactsDir := filepath.Join(dir, "contacts")
	setEnv(t, settingEnv(contacts.DirKey), contactsDir)
	if err := os.MkdirAll(filepath.Join(contactsDir, "card"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contactsDir, "card", "alice.vcf"), []byte(aliceCard), 0o666); err != nil {
		t.Fatal(err)
	}
	start := warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "contacts_lookup", map[string]any{"query": "Alice", "include": []any{"emails"}})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Email Alice"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("n1", "notify", map[string]any{"title": "Alice", "body": "Found her"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": "https://example.com/"})),
	)
	if ran := runApp(t, "agent", "run", "--text", "find Alice and email her"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 4 {
		t.Fatalf("%d model requests, want 4", len(requests))
	}
	if after := requestTools(t, requests[1]); !slices.Contains(after, "task_add") || !slices.Contains(after, "notify") || !slices.Contains(after, "web_fetch") {
		t.Errorf("own, notify and internet tools after a pii read: %v", after)
	}
	for _, id := range []string{"t1", "n1"} {
		if result := toolResultFor(t, requests[len(requests)-1], id); result["is_error"] == true {
			t.Errorf("%s failed after a pii read: %v", id, result)
		}
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if marks := threadMarkSet(t, dir, latestThread(t, dir)); marks != int64(tool.PII) {
		t.Errorf("thread marks = %d, want pii", marks)
	}
	thread := latestThread(t, dir)
	waitRunState(t, d, thread, "parked")
	var asked string
	if err := d.Read.QueryRow("SELECT json_extract(payload, '$.tool') FROM proposals WHERE id = ?", permissionProposal(t, d)).Scan(&asked); err != nil || asked != "web_fetch" {
		t.Errorf("permission request for %q, %v, want web_fetch", asked, err)
	}
	var tasks, notifications int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE title = 'Email Alice'").Scan(&tasks); err != nil || tasks != 1 {
		t.Errorf("tasks = %d, %v, want one", tasks, err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications WHERE title = 'Alice'").Scan(&notifications); err != nil || notifications != 1 {
		t.Errorf("notifications = %d, %v, want one", notifications, err)
	}
}

func TestTaskWrittenFromAMarkedThreadMarksItsReader(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Book the room"})),
		anthropictest.Reply("end_turn", anthropictest.Text("noted")),
	)
	if ran := runApp(t, "agent", "run", "--text", "find a slot and remind me"); ran.code != cli.ExitOK {
		t.Fatalf("writer run: %d %s", ran.code, ran.stderr)
	}
	writer := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, writer); marks != int64(tool.Messages) {
		t.Fatalf("writer marks = %d, want messages", marks)
	}

	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "commitment_list", map[string]any{})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": "https://example.com/"})),
		anthropictest.Reply("end_turn", anthropictest.Text("listed")),
	)
	if ran := runApp(t, "agent", "run", "--text", "list my tasks"); ran.code != cli.ExitOK {
		t.Fatalf("reader run: %d %s", ran.code, ran.stderr)
	}
	reader := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, reader); marks != int64(tool.Messages) {
		t.Errorf("reader marks = %d, want messages inherited from the task writer", marks)
	}
	if after := requestTools(t, api.Requests()[start+1]); !slices.Contains(after, "web_fetch") {
		t.Errorf("web_fetch not offered after listing an inherited task: %v", after)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	waitRunState(t, d, reader, "parked")
	if id := permissionProposal(t, d); id == 0 {
		t.Error("no permission request after the gated fetch")
	}
}

func TestScheduleFromAMarkedThreadStartsMarked(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "warm"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "schedule_create", map[string]any{"name": "Room check", "prompt": "Check the room.", "schedule": "daily 09:00"})),
		anthropictest.Reply("end_turn", anthropictest.Text("scheduled")),
	)
	if ran := runApp(t, "agent", "run", "--text", "find a slot and schedule a check"); ran.code != cli.ExitOK {
		t.Fatalf("writer run: %d %s", ran.code, ran.stderr)
	}
	writer := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, writer); marks != int64(tool.Messages) {
		t.Fatalf("writer marks = %d, want messages", marks)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var job int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'Room check'").Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE jobs SET next_due = 0 WHERE id = ?", job); err != nil {
		t.Fatal(err)
	}
	start := len(api.Requests())
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("checked")))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() {
		done <- runAppContext(t, f, ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	}()
	var thread int64
	for deadline := time.Now().Add(20 * time.Second); ; {
		err := d.Read.QueryRow("SELECT thread_id FROM job_runs WHERE job_id = ? AND thread_id IS NOT NULL ORDER BY id DESC LIMIT 1", job).Scan(&thread)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("the scheduled run never started: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitRunState(t, d, thread, "done")
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the scheduler ignored cancellation")
	}
	if marks := threadMarkSet(t, dir, thread); marks != int64(tool.Messages) {
		t.Errorf("cron thread marks = %d, want messages inherited from the schedule writer", marks)
	}
	requests := api.Requests()[start:]
	if first := requestTools(t, requests[0]); slices.Contains(first, "web_fetch") {
		t.Errorf("cron run offered web_fetch in situation b: %v", first)
	}
	if first := requestTools(t, requests[0]); !slices.Contains(first, "notify") || !slices.Contains(first, "task_add") {
		t.Errorf("cron run lost own and notify tools: %v", first)
	}
}

func TestScheduledRunAfterMailReadNotifiesButCannotFetch(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	srv, pages := webPages(t)
	warm(t, api)
	message := seedMail(t, dir)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("m1", "mail_read", map[string]any{"message_id": message})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", "schedule_create", map[string]any{"name": "Mail check", "prompt": "Check " + srv.URL + "/a for news.", "schedule": "daily 10:00"})),
		anthropictest.Reply("end_turn", anthropictest.Text("scheduled")),
	)
	if ran := runApp(t, "agent", "run", "--text", "read my mail and schedule a check"); ran.code != cli.ExitOK {
		t.Fatalf("writer run: %d %s", ran.code, ran.stderr)
	}
	writer := latestThread(t, dir)
	if marks := threadMarkSet(t, dir, writer); marks == 0 {
		t.Fatal("the writer thread is unmarked after a mail read")
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var job int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'Mail check'").Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE jobs SET next_due = 0 WHERE id = ?", job); err != nil {
		t.Fatal(err)
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("n1", "notify", map[string]any{"title": "Mail check", "body": "Nothing new"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "web_fetch", map[string]any{"url": srv.URL + "/a"})),
		anthropictest.Reply("end_turn", anthropictest.Text("checked")),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() {
		done <- runAppContext(t, f, ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	}()
	var thread int64
	for deadline := time.Now().Add(20 * time.Second); ; {
		err := d.Read.QueryRow("SELECT thread_id FROM job_runs WHERE job_id = ? AND thread_id IS NOT NULL ORDER BY id DESC LIMIT 1", job).Scan(&thread)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("the scheduled run never started: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitRunState(t, d, thread, "done")
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the scheduler ignored cancellation")
	}
	requests := api.Requests()[start:]
	if first := requestTools(t, requests[0]); slices.Contains(first, "web_fetch") {
		t.Errorf("cron run after a mail read offered web_fetch: %v", first)
	}
	if result := toolResultFor(t, requests[len(requests)-1], "n1"); result["is_error"] == true {
		t.Errorf("notify failed in the cron run: %v", result)
	}
	refusal := toolResultFor(t, requests[len(requests)-1], "f1")
	if refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "web_fetch") {
		t.Errorf("cron web_fetch returned %v, want a refusal", refusal)
	}
	if hits := pages.seen(); len(hits) != 0 {
		t.Errorf("the cron run reached the web: %v", hits)
	}
}
