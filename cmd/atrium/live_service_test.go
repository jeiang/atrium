package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestServeChatAndAgentTickShareOneRunAuthority(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	before := len(api.Requests())
	release := asstHold(t, api)
	base, _, stop := startServeStoppable(t, dir)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	w := asstBrowse(t, base)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("held", "profile_get", map[string]any{"key": "k"})))
	thread := w.start(map[string]string{"text": "authoritymarker"})
	asstWaitRequests(t, api, before+1)
	run := asstLastRun(t, d, thread)
	owner := liveRun(t, d, run)

	tick := func(what string) {
		t.Helper()
		next, stderr := tickProcess(t)
		if code := exitCode(next.Wait()); code != 0 {
			t.Fatalf("%s tick exit %d: %s", what, code, stderr)
		}
	}
	tick("live")
	if n := len(api.Requests()) - before; n != 1 {
		t.Fatalf("%d model requests after a tick while serve holds the chat run, want 1", n)
	}
	if got := liveRun(t, d, run); got != owner {
		t.Fatalf("chat run %d moved from %s to %s during the tick", run, owner, got)
	}

	stopWhileHeld(t, stop, release)
	waitUntil(t, 10*time.Second, "the in-flight reply", func() bool { return api.Remaining() == 0 })
	var state string
	if err := d.Read.QueryRow("SELECT state FROM runs WHERE id = ?", run).Scan(&state); err != nil || state != "running" && state != "interrupted" {
		t.Fatalf("chat run %s after serve stopped, want it left resumable: %v", state, err)
	}
	asstExec(t, d, "UPDATE runs SET lease_until = ? WHERE id = ? AND state = 'running'", unixNow()-1, run)

	api.Push(asstReply("resumed reply"))
	tick("restart")
	if state, text := asstSettle(t, d, run); state != "done" {
		t.Fatalf("chat run %s %s after the restart tick, want done", state, text)
	}
	if n := len(api.Requests()) - before; n != 2 {
		t.Fatalf("%d model requests after the restart tick, want 2", n)
	}
	w = asstBrowse(t, startServeIn(t, dir))
	code, body, _ := w.post(fmt.Sprintf("/chat/%d/runs/%d/resume", thread, run), url.Values{}, true)
	w.want(code, http.StatusConflict, body, "run is not interrupted")
	tick("settled")
	if n := len(api.Requests()) - before; n != 2 {
		t.Fatalf("%d model requests after serve restarted and a tick on the settled run, want 2", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id = ?", thread); n != 1 {
		t.Fatalf("%d runs on the chat thread, want 1", n)
	}
}

func TestAssistantWithoutTheAnthropicKeyRefusesPaidRunsAndKeepsModelFreeWork(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	host := filepath.Join(dir, "host-no-anthropic.toml")
	body := hostConfigBodyWith(defaultEmbeddingConnection, defaultWebPushConnection, "") + defaultQbitConnection + mailConnection("icloud", "icloud", "operator@icloud.invalid", defaultMailPassword, true)
	if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["ATRIUM_HOST_CONFIG"] = host
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	asstExec(t, d, "INSERT INTO tasks (title, notes, remind_at, status, priority, source_kind, source_id, created_ts, updated_ts) VALUES ('water the plants', '', ?, 'open', 'normal', '', '', ?, ?)", unixNow()-60, unixNow()-120, unixNow()-120)
	schedulerDue(t, d, "weekly-review", "task-reminders")

	next, stderr := tickProcess(t)
	if code := exitCode(next.Wait()); code != 0 {
		t.Fatalf("tick exit %d: %s", code, stderr)
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "skipped_unconfigured" || !strings.Contains(text, "Anthropic") {
		t.Errorf("weekly review %s %q, want skipped_unconfigured naming the Anthropic connection", status, text)
	}
	if status, text := schedulerStatus(t, d, "task-reminders"); status != "ok" {
		t.Errorf("task reminders %s %q, want ok", status, text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tasks WHERE title = 'water the plants' AND reminded_ts IS NOT NULL"); n != 1 {
		t.Errorf("the due task reminder was not sent")
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE state = 'done'"); n != 0 {
		t.Errorf("%d runs finished without an Anthropic key, want 0", n)
	}

	w := asstBrowse(t, startServeIn(t, dir))
	for _, page := range []string{"/scheduled", "/settings/assistant", "/settings/export", "/settings/connections"} {
		code, body, _ := w.get(page, false)
		w.want(code, http.StatusOK, body)
	}
	thread := w.start(map[string]string{"text": "paidmarker"})
	if state := asstSettleThread(t, d, thread); state == "done" {
		t.Errorf("a chat run finished without an Anthropic key")
	}
	_, text := asstSettle(t, d, asstLastRun(t, d, thread))
	if !strings.Contains(text, "Anthropic") {
		t.Errorf("chat run error %q, want it to name the missing Anthropic connection", text)
	}
	if n := len(api.Requests()); n != 0 {
		t.Errorf("%d model requests without an Anthropic key, want 0", n)
	}
}

func stopWhileHeld(t *testing.T, stop, release func()) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("serve stopped while a model request was in flight")
	case <-time.After(2 * time.Second):
	}
	release()
	<-stopped
}
