package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

func TestAsstRunHeldOnAnUncertainWriteShowsItAndResumesWithoutReplaying(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, stop := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("w-held", "task_add", map[string]any{"title": "Renew the passport"})),
		asstReply("added"),
	)
	thread := webStartG1(t, w, map[string]any{"text": "add the passport task"})
	run := asstLastRun(t, d, thread)
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("first run %s %q", state, errText)
	}
	stop()

	var useSeq int64
	if err := d.Read.QueryRow("SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND role = 'assistant'", thread).Scan(&useSeq); err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "DELETE FROM thread_messages WHERE thread_id = ? AND seq > ?", thread, useSeq)
	asstExec(t, d, "UPDATE tool_calls SET state = 'started', result = NULL, result_id = NULL, error = NULL, finished_at = NULL WHERE run_id = ? AND tool_use_id = 'w-held'", run)
	asstExec(t, d, "UPDATE runs SET state = 'interrupted', ended_at = NULL, error = NULL, owner = NULL, lease_until = NULL, stop_requested = 1 WHERE id = ?", run)
	for _, text := range []string{"keep going", "and then report"} {
		asstExec(t, d, "INSERT INTO thread_queue (thread_id, text, created_at, files_json) VALUES (?, ?, ?, '[]')", thread, text, unixNow())
	}

	sent := len(api.Requests())
	w = localWebG1(t, startServeIn(t, dir))
	view := webOKG1(t, w.do(http.MethodGet, threadG1(thread, ""), nil))
	active, _ := view["active_run"].(map[string]any)
	held, _ := view["held"].(map[string]any)
	queued, _ := view["queued"].([]any)
	if active == nil || active["state"] != "interrupted" || number(t, active["id"]) != run {
		t.Fatalf("active run after restart %v, want run %d interrupted", view["active_run"], run)
	}
	if held == nil || held["tool"] != "task_add" || held["tool_use_id"] != "w-held" || number(t, held["run_id"]) != run ||
		!strings.Contains(fmt.Sprint(held["args"]), "Renew the passport") {
		t.Fatalf("held call %v, want task_add on run %d with its arguments", view["held"], run)
	}
	if len(queued) != 2 {
		t.Fatalf("queued %v, want both messages waiting", view["queued"])
	}
	if n := len(api.Requests()); n != sent {
		t.Fatalf("serve resumed the held run on its own: %d model requests", n-sent)
	}

	api.Push(asstReply("checked and reported"))
	webWantG1(t, w.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/runs/%d/resume", thread, run), nil), http.StatusAccepted)
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("resumed run %s %q, want done", state, errText)
	}
	requests := api.Requests()
	if len(requests) != sent+1 {
		t.Fatalf("%d model requests after resume, want 1", len(requests)-sent)
	}
	body := string(requests[sent].Body)
	for _, want := range []string{"may or may not have taken effect", "keep going", "and then report"} {
		if !strings.Contains(body, want) {
			t.Errorf("the resumed request lacks %q", want)
		}
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tasks WHERE title = 'Renew the passport'"); n != 1 {
		t.Errorf("the task exists %d times, want once: the uncertain write was replayed", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_use_id = 'w-held' AND state = 'unknown'", run); n != 1 {
		t.Errorf("the uncertain call was not recorded as unknown")
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NOT NULL AND run_id = ?", thread, run); n != 2 {
		t.Errorf("%d queued messages consumed by the resumed run, want 2", n)
	}
	view = webOKG1(t, w.do(http.MethodGet, threadG1(thread, ""), nil))
	if view["held"] != nil || view["active_run"] != nil || len(view["queued"].([]any)) != 0 {
		t.Errorf("after the resumed run: held %v active %v queued %v", view["held"], view["active_run"], view["queued"])
	}
}

func TestAsstAStoppedRunLeavesNoDanglingCallsAndItsQueueJoinsTheNextSendInOrder(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, _ := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(anthropictest.Reply("tool_use",
		anthropictest.ToolUse("s-1", "task_add", map[string]any{"title": "Sand the deck"}),
		anthropictest.ToolUse("s-2", "task_add", map[string]any{"title": "Oil the deck"}),
	))
	release := asstHold(t, api)
	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "long job"})
	asstWaitRequests(t, api, sent+1)
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "earlier note"}), http.StatusAccepted)
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/stop"), nil), http.StatusOK)
	release()
	run := asstLastRun(t, d, thread)
	if state, errText := asstSettle(t, d, run); state != "failed" || !strings.Contains(errText, "stop") {
		t.Fatalf("stopped run %s %q", state, errText)
	}
	view := webOKG1(t, w.do(http.MethodGet, threadG1(thread, ""), nil))
	if queued, _ := view["queued"].([]any); view["active_run"] != nil || len(queued) != 1 {
		t.Fatalf("after the stop: active %v queued %v, want the note left queued", view["active_run"], view["queued"])
	}

	api.Push(asstReply("second"))
	sent = len(api.Requests())
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "new note"}), http.StatusAccepted)
	next := asstLastRun(t, d, thread)
	if next == run {
		t.Fatal("the send started no run")
	}
	if state, errText := asstSettle(t, d, next); state != "done" {
		t.Fatalf("next run %s %q", state, errText)
	}
	requests := api.Requests()
	if len(requests) != sent+1 {
		t.Fatalf("%d model requests for the send, want 1", len(requests)-sent)
	}
	body := string(requests[sent].Body)
	for _, id := range []string{`"s-1"`, `"s-2"`} {
		if n := strings.Count(body, id); n != 2 {
			t.Errorf("call %s appears %d times in the next request, want its use and its result", id, n)
		}
	}
	results, earlier, latest := strings.LastIndex(body, `"s-2"`), strings.Index(body, "earlier note"), strings.Index(body, "new note")
	if results < 0 || earlier < 0 || latest < 0 || results > earlier || earlier > latest {
		t.Errorf("want the stopped calls' results, then the queued note, then the new message: at %d, %d, %d", results, earlier, latest)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM tasks WHERE title IN ('Sand the deck', 'Oil the deck')"); n != 0 {
		t.Errorf("%d tasks written by calls of the stopped run, want none", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NOT NULL AND run_id = ?", thread, next); n != 1 {
		t.Errorf("%d queued messages consumed by the next run, want 1", n)
	}
}
