package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

const nightlyChildren = "SELECT COUNT(*) FROM job_run_threads t JOIN job_runs r ON r.id = t.job_run_id JOIN jobs j ON j.id = r.job_id WHERE j.name = 'nightly'"

func twoDreamingGroups(t *testing.T) (*db.DB, *modelLog, int) {
	t.Helper()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	for i, text := range []string{"I prefer green tea", "the plumber is coming on Friday"} {
		if i > 0 {
			api.Push(asstReply("noted"))
		}
		if ran := runApp(t, "agent", "run", "--text", text); ran.code != cli.ExitOK {
			t.Fatalf("chat run: %d %s", ran.code, ran.stderr)
		}
	}
	d := mustConnect(t, dir+"/atrium.db")
	asstExec(t, d, "UPDATE threads SET marks = 5, tainted_at = ? WHERE id = ?", unixNow(), chatThread(t, d))
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	schedulerDue(t, d, "nightly")
	return d, api, len(api.Requests())
}

func holdNth(t *testing.T, api *modelLog, n int64) func() {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	var seen atomic.Int64
	prev := api.OnRequest
	api.OnRequest = func(r anthropictest.Request) {
		prev(r)
		if seen.Add(1) == n {
			<-gate
		}
	}
	return release
}

func TestAgentTickFailsAJobWhoseLaterDreamingChildFails(t *testing.T) {
	t.Parallel()
	d, api, before := twoDreamingGroups(t)
	api.Push(asstReply("wrote nothing"), anthropictest.Failure(400, "invalid_request_error", "second child refused"))
	tick, stderr := tickProcess(t)
	if code := exitCode(tick.Wait()); code != 0 {
		t.Fatalf("tick exit %d: %s", code, stderr)
	}
	if got := len(api.Requests()) - before; got != 2 {
		t.Fatalf("%d dreaming requests, want one per child", got)
	}
	if n := asstCount(t, d, nightlyChildren); n != 2 {
		t.Fatalf("%d associated children, want 2", n)
	}
	if status, text := schedulerStatus(t, d, "nightly"); status != "error" {
		var states string
		_ = d.Read.QueryRow("SELECT group_concat(t.kind || '/' || r.id || ':' || r.state || ':' || COALESCE(r.error, ''), ' | ') FROM runs r JOIN threads t ON t.id = r.thread_id").Scan(&states)
		states += fmt.Sprintf(" requests=%d before=%d", len(api.Requests()), before)
		t.Fatalf("nightly status %s %s, want error; children %s", status, text, states)
	}
}

func TestAgentTickSettlesALateDreamingChildAfterRestart(t *testing.T) {
	t.Parallel()
	d, api, before := twoDreamingGroups(t)
	api.Push(asstReply("first done"), asstReply("second interrupted"))
	release := holdNth(t, api, 2)
	first, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "the held second child", func() bool { return len(api.Requests())-before == 2 })
	if n := asstCount(t, d, nightlyChildren); n != 2 {
		t.Fatalf("%d associated children before the late child ran, want 2", n)
	}
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	release()
	if status, text := schedulerStatus(t, d, "nightly"); status != "running" {
		t.Fatalf("nightly status after cancel %s %s, want running: %s", status, text, stderr)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs r JOIN threads t ON t.id = r.thread_id WHERE t.kind = 'nightly' AND r.state = 'done'"); n != 1 {
		t.Fatalf("%d finished children after cancel, want the first", n)
	}
	api.Push(asstReply("second done"))
	next, nextErr := tickProcess(t)
	if code := exitCode(next.Wait()); code != 0 {
		t.Fatalf("recovery tick exit %d: %s", code, nextErr)
	}
	if status, text := schedulerStatus(t, d, "nightly"); status != "ok" {
		t.Fatalf("nightly status %s %s, want ok", status, text)
	}
	if got := len(api.Requests()) - before; got != 3 {
		t.Fatalf("%d dreaming requests, want the first child once and the second resumed", got)
	}
	if n := schedulerRuns(t, d, "nightly"); n != 1 {
		t.Fatalf("%d nightly runs, want one occurrence", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs r JOIN threads t ON t.id = r.thread_id WHERE t.kind = 'nightly' AND r.state <> 'done'"); n != 0 {
		t.Fatalf("%d unsettled children, want none", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases"); n != 0 {
		t.Fatalf("%d resource leases after settlement, want none", n)
	}
}
