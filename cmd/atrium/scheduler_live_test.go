package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/db"
)

const operatorMarker = "operatormarker"

type heldGates struct {
	gates []chan struct{}
	once  []sync.Once
}

func newHeldGates(t *testing.T, n int) *heldGates {
	g := &heldGates{gates: make([]chan struct{}, n), once: make([]sync.Once, n)}
	for i := range g.gates {
		g.gates[i] = make(chan struct{})
		t.Cleanup(func() { g.release(i) })
	}
	return g
}

func (g *heldGates) release(i int) { g.once[i].Do(func() { close(g.gates[i]) }) }

func liveRun(t *testing.T, d *db.DB, run int64) string {
	t.Helper()
	var owner string
	if err := d.Read.QueryRow("SELECT COALESCE(owner, '') FROM runs WHERE id = ? AND state = 'running' AND lease_until > ?", run, unixNow()).Scan(&owner); err != nil {
		t.Fatalf("run %d is not live: %v", run, err)
	}
	return owner
}

func TestAgentTickLeavesAnotherProcessLiveRunOnTheJobThread(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	api.Push(asstReply("interrupted"))
	before := len(api.Requests())
	schedulerDue(t, d, "weekly-review")

	const tickHeld, resumeHeld, operatorHeld = 0, 1, 2
	g := newHeldGates(t, 3)
	var jobSeen, operatorSeen atomic.Int64
	logRequest := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		logRequest(r)
		if bytes.Contains(r.Body, []byte(operatorMarker)) {
			if operatorSeen.Add(1) == 1 {
				<-g.gates[operatorHeld]
			}
			return
		}
		if n := jobSeen.Add(1); n <= 2 {
			<-g.gates[n-1]
		}
	})
	tick := func(what string) {
		t.Helper()
		next, stderr := tickProcess(t)
		if code := exitCode(next.Wait()); code != 0 {
			t.Fatalf("%s tick exit %d: %s", what, code, stderr)
		}
	}

	first, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "the held job request", func() bool { return jobSeen.Load() == 1 })
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	g.release(tickHeld)
	waitUntil(t, 10*time.Second, "the abandoned job reply", func() bool { return api.Remaining() == 0 })
	if n := asstCount(t, d, weeklyRunning); n != 1 {
		t.Fatalf("%d running weekly reviews after SIGTERM, want 1: %s", n, stderr)
	}
	jobRun := weeklyLeaseOf(t, d).run
	var thread int64
	if err := d.Read.QueryRow("SELECT thread_id FROM job_run_threads WHERE job_run_id = ?", jobRun).Scan(&thread); err != nil {
		t.Fatal(err)
	}
	ownRun := asstLastRun(t, d, thread)

	w := asstBrowse(t, startWebOnlyIn(t, dir))
	api.Push(asstReply("reviewed in serve"))
	code, body, _ := w.post(fmt.Sprintf("/chat/%d/runs/%d/resume", thread, ownRun), url.Values{}, true)
	w.want(code, http.StatusOK, body)
	waitUntil(t, 20*time.Second, "serve's held resume", func() bool { return jobSeen.Load() == 2 })
	resumer := liveRun(t, d, ownRun)

	tick("live resume")
	if got := len(api.Requests()) - before; got != 2 {
		t.Fatalf("%d model requests while serve resumes the job run, want 2", got)
	}
	if owner := liveRun(t, d, ownRun); owner != resumer {
		t.Fatalf("job run %d moved from %s to %s", ownRun, resumer, owner)
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "running" {
		t.Fatalf("weekly review %s %s while serve runs it, want running", status, text)
	}
	g.release(resumeHeld)
	if state, text := asstSettle(t, d, ownRun); state != "done" {
		t.Fatalf("job run %s %s, want done in serve", state, text)
	}

	spent := asstReply("operator answer")
	spent.Usage = json.RawMessage(`{"input_tokens":50000,"output_tokens":100}`)
	api.Push(spent)
	code, body, _ = w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": operatorMarker}, nil, true)
	w.want(code, http.StatusOK, body)
	waitUntil(t, 20*time.Second, "the held operator request", func() bool { return operatorSeen.Load() == 1 })
	operatorRun := asstLastRun(t, d, thread)
	if operatorRun == ownRun {
		t.Fatalf("the operator message started no run in job thread %d", thread)
	}
	operator := liveRun(t, d, operatorRun)

	tick("settling")
	if got := len(api.Requests()) - before; got != 3 || operatorSeen.Load() != 1 {
		t.Fatalf("%d model requests (%d operator) after the tick, want 3 with serve's operator request only", got, operatorSeen.Load())
	}
	if owner := liveRun(t, d, operatorRun); owner != operator {
		t.Fatalf("operator run %d moved from %s to %s", operatorRun, operator, owner)
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s %s, want ok from its own run", status, text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE id = ? AND thread_id = ? AND tokens > 0 AND tokens < 50000", jobRun, thread); n != 1 {
		t.Fatal("the job outcome does not come from the job's own run alone")
	}

	g.release(operatorHeld)
	if state, text := asstSettle(t, d, operatorRun); state != "done" {
		t.Fatalf("operator run %s %s, want done in serve", state, text)
	}
	if got := len(api.Requests()) - before; got != 3 || operatorSeen.Load() != 1 {
		t.Fatalf("%d model requests (%d operator), want 3 with one operator request", got, operatorSeen.Load())
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
}
