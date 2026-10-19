package service_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

const lapsed = now0 + journal.LeaseSeconds + 1

func (w *world) prepare(text string, at float64) (int64, int64) {
	w.t.Helper()
	thread := w.thread(threads.Chat)
	run, err := w.svc.Prepare(w.t.Context(), thread, text, at, service.Start{})
	if err != nil {
		w.t.Fatal(err)
	}
	return thread, run
}

func (w *world) assistant(thread, run int64, blocks ...json.RawMessage) {
	w.t.Helper()
	if _, err := journal.AppendMessage(w.t.Context(), w.d, thread, &run, journal.Assistant, blocks, now0, nil); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) startCall(run int64, id, name, args, class string) int64 {
	w.t.Helper()
	call, err := journal.StartToolCall(w.t.Context(), w.d.Write, run, id, name, json.RawMessage(args), class, now0)
	if err != nil {
		w.t.Fatal(err)
	}
	return call
}

func (w *world) resumeInterrupted(at float64) []service.Resumed {
	w.t.Helper()
	w.svc.Now = clock(at)
	out, err := w.svc.ResumeInterrupted(w.t.Context(), false)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// interruptedRun is interrupted_run: a write call left started (or finished in writeState) and a safe call.
func interruptedRun(w *world, writeState string) (int64, int64) {
	thread, run := w.prepare("save this", now0)
	w.assistant(thread, run, use("w1", "note_write", map[string]any{"body": "x"}), use("s1", "probe", map[string]any{"id": "s1"}))
	call := w.startCall(run, "w1", "note_write", `{"body":"x"}`, journal.Write)
	if writeState != journal.Started {
		var errText *string
		if writeState == journal.Error {
			errText = new("boom")
		}
		if err := journal.FinishToolCall(w.t.Context(), w.d.Write, call, writeState, now0, errText, nil); err != nil {
			w.t.Fatal(err)
		}
	}
	w.startCall(run, "s1", "probe", `{"id":"s1"}`, journal.Safe)
	return thread, run
}

func TestRestartMarksRunningRunsInterruptedAndResumeReconciles(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	_, run := interruptedRun(w, journal.Started)
	w.push(reply(text("checked")))
	if got := w.resumeInterrupted(lapsed); !slices.Equal(got, []service.Resumed{{RunID: run, Action: service.AwaitingOperator}}) {
		t.Fatalf("outcome %+v", got)
	}
	if w.run(run).State != journal.Interrupted || len(w.srv.Requests()) != 0 {
		t.Fatal("a run awaiting the operator was continued")
	}
	w.svc.Now = clock(now0)
	if _, err := w.svc.ResumeRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if w.run(run).State != journal.Done {
		t.Fatalf("run %+v", w.run(run))
	}
	results := path(w.body(0), "messages", 2, "content").([]any)
	if path(results, 0, "tool_use_id") != "w1" || path(results, 0, "is_error") != true || !strings.Contains(path(results, 0, "content").(string), "may or may not") {
		t.Errorf("write result %v", results[0])
	}
	var probed map[string]any
	if err := json.Unmarshal([]byte(path(results, 1, "content").(string)), &probed); err != nil || probed["journaled"] != true {
		t.Errorf("safe result %v", results[1])
	}
	for id, state := range map[string]string{"w1": journal.Unknown, "s1": journal.OK} {
		if call, _ := journal.GetToolCall(t.Context(), w.d.Read, run, id); call == nil || call.State != state {
			t.Errorf("call %s: %+v", id, call)
		}
	}
	if len(writeCalls) != 0 {
		t.Errorf("the write ran again: %v", writeCalls)
	}
}

func TestSettledWritesReportTheirRecordedOutcome(t *testing.T) {
	for state, want := range map[string]string{journal.Error: "boom", journal.OK: loop.CompletedUncaptured} {
		w := newWorld(t, loopTools(t)...)
		_, run := interruptedRun(w, state)
		w.push(reply(text("checked")))
		if got := w.resumeInterrupted(lapsed); !slices.Equal(got, []service.Resumed{{RunID: run, Action: service.ActionResumed}}) {
			t.Fatalf("%s: outcome %+v", state, got)
		}
		if got := path(w.body(0), "messages", 2, "content", 0); path(got, "content") != want || path(got, "is_error") != true {
			t.Errorf("%s: %v", state, got)
		}
	}
}

func TestSafeOnlyTailsResumeAutomatically(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	thread, run := w.prepare("find", now0)
	w.assistant(thread, run, use("s1", "probe", map[string]any{"id": "s1"}))
	w.push(reply(text("found")))
	if got := w.resumeInterrupted(lapsed); !slices.Equal(got, []service.Resumed{{RunID: run, Action: service.ActionResumed}}) {
		t.Fatalf("outcome %+v", got)
	}
	found := w.run(run)
	if found.State != journal.Done || found.Owner == nil || *found.Owner != loop.Owner || len(w.srv.Requests()) != 1 {
		t.Errorf("run %+v", found)
	}
}

func TestLiveLeaseIsNeverInterrupted(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	thread, run := w.prepare("find", now0)
	w.assistant(thread, run, use("s1", "probe", map[string]any{"id": "s1"}))
	w.push(reply(text("found")))
	if got := w.resumeInterrupted(now0 + 10); len(got) != 0 {
		t.Fatalf("outcome %+v", got)
	}
	if found := w.run(run); found.State != journal.Running || *found.Owner != loop.Owner || len(w.srv.Requests()) != 0 {
		t.Fatalf("run %+v", found)
	}
	if n, _ := journal.MarkInterrupted(t.Context(), w.d.Write, now0+journal.LeaseSeconds-1); n != 0 {
		t.Errorf("marked %d before the lease lapsed", n)
	}
	if n, _ := journal.MarkInterrupted(t.Context(), w.d.Write, lapsed); n != 1 {
		t.Errorf("marked %d after the lease lapsed", n)
	}
	if got := w.resumeInterrupted(lapsed); !slices.Equal(got, []service.Resumed{{RunID: run, Action: service.ActionResumed}}) {
		t.Fatalf("outcome %+v", got)
	}
	if w.run(run).State != journal.Done {
		t.Error("not done")
	}
}

func TestResumeClaimIsAtomic(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	thread, run := w.prepare("find", now0)
	w.assistant(thread, run, use("s1", "probe", map[string]any{"id": "s1"}))
	if _, err := journal.MarkInterrupted(t.Context(), w.d.Write, lapsed); err != nil {
		t.Fatal(err)
	}
	if ok, _ := journal.ClaimResume(t.Context(), w.d.Write, run, "web:other", lapsed); !ok {
		t.Fatal("first claim failed")
	}
	if ok, _ := journal.ClaimResume(t.Context(), w.d.Write, run, loop.Owner, lapsed); ok {
		t.Fatal("second claim succeeded")
	}
	w.svc.Now = clock(lapsed)
	_, err := w.svc.ResumeRun(t.Context(), run)
	if !errors.Is(err, service.NotInterrupted(run)) || !strings.Contains(err.Error(), "not interrupted") {
		t.Fatalf("resume: %v", err)
	}
	if got := w.resumeInterrupted(lapsed); len(got) != 0 {
		t.Errorf("outcome %+v", got)
	}
	if found := w.run(run); *found.Owner != "web:other" {
		t.Errorf("owner %v", *found.Owner)
	}
}

func TestLoopRenewsItsLeaseAndStopsWhenTakenOver(t *testing.T) {
	w := newWorld(t, loopTools(t)...)
	w.push(toolTurn(use("a", "probe", map[string]any{"id": "a"})), reply(text("ok")))
	thread, run := w.prepare("go", now0)
	before := *w.run(run).LeaseUntil
	w.svc.Now = clock(now0 + 100)
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{}); err != nil {
		t.Fatal(err)
	}
	if after := w.run(run); after.State != journal.Done || *after.LeaseUntil <= before {
		t.Fatalf("run %+v", after)
	}
	run, err := w.svc.Prepare(t.Context(), thread, "again", now0+200, service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Write.Exec("UPDATE runs SET owner = 'scheduler:other' WHERE id = ?", run); err != nil {
		t.Fatal(err)
	}
	requests := len(w.srv.Requests())
	w.svc.Now = clock(now0 + 300)
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{}); err != nil {
		t.Fatal(err)
	}
	found := w.run(run)
	if found.State != journal.Running || *found.Owner != "scheduler:other" || found.Error != nil {
		t.Errorf("run %+v", found)
	}
	messages := w.messages(thread)
	if len(w.srv.Requests()) != requests || messages[len(messages)-1].Role != journal.User {
		t.Error("the taken-over run kept going")
	}
}
