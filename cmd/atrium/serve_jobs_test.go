package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
)

type heldQBittorrent struct {
	*httptest.Server
	listed  atomic.Int64
	release chan struct{}
}

func newHeldQBittorrent(t *testing.T, held bool) *heldQBittorrent {
	t.Helper()
	q := &heldQBittorrent{release: make(chan struct{})}
	if !held {
		close(q.release)
	}
	q.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/torrents/info" {
			http.NotFound(w, r)
			return
		}
		q.listed.Add(1)
		select {
		case <-q.release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode([]media.Torrent{})
	}))
	t.Cleanup(func() {
		select {
		case <-q.release:
		default:
			close(q.release)
		}
		q.Close()
	})
	return q
}

func sweepRuns(t *testing.T, d *db.DB) int {
	t.Helper()
	return schedulerRuns(t, d, media.JobSweep)
}

func sweepNextDue(t *testing.T, d *db.DB) *float64 {
	t.Helper()
	var due *float64
	if err := d.Read.QueryRow("SELECT next_due FROM jobs WHERE declaration_key = ?", module.Media+"/"+media.JobSweep).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}

func declareJobsAndParkThem(t *testing.T, d *db.DB) {
	t.Helper()
	if ran := runApp(t, "agent", "tick"); ran.code != cli.ExitOK {
		t.Fatalf("agent tick to declare the jobs: exit %d stderr %q", ran.code, ran.stderr)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs"); n != 0 {
		t.Fatalf("%d job runs after declaring the jobs, want 0", n)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
}

func TestServeRunsModelFreeJobsWithAssistantDisabled(t *testing.T) {
	qbit := newHeldQBittorrent(t, false)
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbit.URL, "", "", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	asstSet(t, module.FlagKey(module.Assistant), "false")
	before := len(api.Requests())
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", float64(time.Now().Unix())-1, media.JobSweep)

	startServeIn(t, f.dir)
	waitUntil(t, 30*time.Second, "the media sweep run in serve", schedulerTerminal(d, media.JobSweep))
	if status, text := schedulerStatus(t, d, media.JobSweep); status != "ok" {
		t.Fatalf("media sweep status %s: %s", status, text)
	}
	if qbit.listed.Load() == 0 {
		t.Fatal("the media sweep never listed torrents")
	}
	if n := len(api.Requests()) - before; n != 0 {
		t.Fatalf("%d model requests with the assistant disabled, want 0", n)
	}
	if due := sweepNextDue(t, d); due == nil || *due <= float64(time.Now().Unix()) {
		t.Fatalf("media sweep next due %v, want a future occurrence", due)
	}
}

func TestManualInvocationAndTickNeverOverlap(t *testing.T) {
	qbit := newHeldQBittorrent(t, true)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbit.URL, "", "", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	due := float64(time.Now().Unix()) - 1
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE name = ?", due, media.JobSweep)

	manual := cliExitProcessArgs(t, []string{"media", "sweep"})
	var manualOut, manualErr bytes.Buffer
	manual.Stdout, manual.Stderr = &manualOut, &manualErr
	if err := manual.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manual.Process.Kill() })
	waitUntil(t, 30*time.Second, "the manual sweep to reach qBittorrent", func() bool { return qbit.listed.Load() > 0 })

	tick, tickErr := tickProcess(t)
	if code := exitWithin(t, tick, 60*time.Second); code != cli.ExitOK && code != cli.ExitTempFail {
		t.Fatalf("agent tick beside the manual sweep: exit %d: %s", code, tickErr)
	}
	second := runApp(t, "media", "sweep")
	if second.code == cli.ExitOK || !strings.Contains(second.stderr, "held by another invocation") {
		t.Fatalf("second manual sweep = (%d, %q), want a held-lock refusal", second.code, second.stderr)
	}
	if n := qbit.listed.Load(); n != 1 {
		t.Fatalf("qBittorrent listed %d times while the manual sweep held the job, want 1", n)
	}

	close(qbit.release)
	if code := exitWithin(t, manual, 60*time.Second); code != cli.ExitOK {
		t.Fatalf("manual sweep exit %d: stdout %q stderr %q", code, manualOut.String(), manualErr.String())
	}
	if n := sweepRuns(t, d); n != 1 {
		t.Fatalf("%d media sweep runs after the manual sweep, want 1", n)
	}
	if status, text := schedulerStatus(t, d, media.JobSweep); status != "ok" {
		t.Fatalf("manual media sweep status %s: %s", status, text)
	}
	if got := sweepNextDue(t, d); got == nil || *got != due {
		t.Fatalf("media sweep next due %v after the manual run, want %v", got, due)
	}

	after, afterErr := tickProcess(t)
	if code := exitWithin(t, after, 60*time.Second); code != cli.ExitOK {
		t.Fatalf("agent tick after the manual sweep: exit %d: %s", code, afterErr)
	}
	if n := sweepRuns(t, d); n != 2 {
		t.Fatalf("%d media sweep runs after the scheduled tick, want 2", n)
	}
	if status, text := schedulerStatus(t, d, media.JobSweep); status != "ok" {
		t.Fatalf("scheduled media sweep status %s: %s", status, text)
	}
	if got := sweepNextDue(t, d); got == nil || *got <= due {
		t.Fatalf("media sweep next due %v after the scheduled run, want later than %v", got, due)
	}
}
