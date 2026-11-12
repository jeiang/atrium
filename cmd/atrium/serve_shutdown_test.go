package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
	"github.com/jeiang/atrium/internal/web"
)

const weeklyReleased = "SELECT COUNT(*) FROM jobs WHERE name = 'weekly-review' AND last_run_id = ? AND COALESCE(lease_until, 0) = 0"

func holdJobRequests(t *testing.T, api *modelLog, n int) (*heldGates, *atomic.Int64) {
	t.Helper()
	g := newHeldGates(t, n)
	var seen atomic.Int64
	logRequest := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		logRequest(r)
		if i := seen.Add(1); i <= int64(n) {
			<-g.gates[i-1]
		}
	})
	return g, &seen
}

func weeklyRunID(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var id int64
	if err := d.Read.QueryRow("SELECT last_run_id FROM jobs WHERE name = 'weekly-review'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func restartServe(t *testing.T, dir string) *serveProcess {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "serve-addr")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return serveCLIProcess(t, dir)
}

func interruptTickDuringWeeklyReview(t *testing.T, api *modelLog, g *heldGates, seen *atomic.Int64, d *db.DB) int64 {
	t.Helper()
	tick, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "the held tick request", func() bool { return seen.Load() == 1 })
	if err := tick.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = tick.Wait()
	remaining := api.Remaining()
	g.release(0)
	waitUntil(t, 10*time.Second, "the abandoned tick reply", func() bool { return api.Remaining() == remaining-1 })
	run := weeklyRunID(t, d)
	if n := asstCount(t, d, weeklyReleased, run); n != 1 {
		t.Fatalf("the interrupted tick did not release weekly review run %d: %s", run, stderr)
	}
	return run
}

func TestServeDrainsTheJobItRecoveredAtStartup(t *testing.T) {
	t.Parallel()
	d, api, f, before := startWeeklyReview(t, asstReply("abandoned"))
	g, seen := holdJobRequests(t, api, 2)

	first := serveCLIProcess(t, f.dir)
	waitUntil(t, 30*time.Second, "the held job request", func() bool { return seen.Load() == 1 })
	if code := first.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("first serve exit %d: %s", code, first.stderr.String())
	}
	g.release(0)
	waitUntil(t, 10*time.Second, "the abandoned reply", func() bool { return api.Remaining() == 0 })
	run := weeklyRunID(t, d)
	released := func(what string, p *serveProcess) {
		t.Helper()
		if text := p.stderr.String(); strings.Contains(text, "database is closed") {
			t.Fatalf("%s wrote after closing its database: %s", what, text)
		}
		if n := asstCount(t, d, weeklyReleased, run); n != 1 {
			t.Fatalf("%s left weekly review run %d claimed: %s", what, run, p.stderr.String())
		}
		if n := asstCount(t, d, weeklyRunning); n != 1 {
			t.Fatalf("%s left %d running weekly reviews, want 1 recoverable run", what, n)
		}
		if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE state = 'running'"); n != 0 {
			t.Fatalf("%s left %d assistant runs running", what, n)
		}
	}
	released("the first serve", first)

	api.Push(asstReply("abandoned again"))
	second := restartServe(t, f.dir)
	waitUntil(t, 30*time.Second, "the recovered job request", func() bool { return seen.Load() == 2 })
	if code := second.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("second serve exit %d: %s", code, second.stderr.String())
	}
	released("the serve that recovered the job", second)
	g.release(1)
	waitUntil(t, 10*time.Second, "the second abandoned reply", func() bool { return api.Remaining() == 0 })

	api.Push(asstReply("reviewed"))
	third := restartServe(t, f.dir)
	waitUntil(t, 60*time.Second, "the recovered weekly review to settle", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s %s, want ok: %s", status, text, third.stderr.String())
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
	if n := len(api.Requests()) - before; n != 3 {
		t.Fatalf("%d weekly review requests, want 3", n)
	}
	if code := third.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("third serve exit %d: %s", code, third.stderr.String())
	}
}

func TestServeStopsClaimingAtSIGTERMWhileShutdownWaits(t *testing.T) {
	t.Parallel()
	d, api, f, before := startWeeklyReview(t)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")

	s := serveCLIProcess(t, f.dir)
	waitUntil(t, 30*time.Second, "the scheduler service", func() bool { return strings.Contains(s.stderr.String(), "service scheduler started") })
	started := time.Now()
	w := asstBrowse(t, "http://"+s.addr)
	body, upload := io.Pipe()
	t.Cleanup(func() { _ = upload.Close() })
	req, err := http.NewRequest(http.MethodPost, w.base+"/chat", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary=held")
	req.Header.Set(web.CSRFHeader, w.token)
	uploaded := make(chan struct{})
	go func() {
		defer close(uploaded)
		if resp, err := w.client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	if _, err := upload.Write([]byte("--held\r\nContent-Disposition: form-data; name=\"text\"\r\n\r\nslow")); err != nil {
		t.Fatal(err)
	}

	tick := time.Duration(scheduler.TuningAgentSchedulerTickSeconds) * time.Second
	time.Sleep(time.Until(started.Add(tick * 5 / 6)))
	schedulerDue(t, d, "weekly-review")
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(started.Add(tick + 3*time.Second)))
	_ = upload.CloseWithError(io.ErrUnexpectedEOF)
	<-uploaded
	if code := exitCode(s.wait()); code != cli.ExitOK {
		t.Fatalf("serve exit %d: %s", code, s.stderr.String())
	}
	if time.Since(started) < tick {
		t.Fatalf("shutdown ended %s after the scheduler started, before its next cycle at %s", time.Since(started).Round(time.Second), tick)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 0 {
		status, text := schedulerStatus(t, d, "weekly-review")
		t.Fatalf("serve claimed the weekly review after SIGTERM: %d runs, last %s %s", n, status, text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM jobs WHERE name = 'weekly-review' AND next_due <= ?", unixNow()); n != 1 {
		t.Fatal("the weekly review occurrence is no longer due after the shutdown")
	}

	api.Push(asstReply("reviewed"))
	next := restartServe(t, f.dir)
	waitUntil(t, 60*time.Second, "the weekly review at the next start", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s %s at the next start, want ok: %s", status, text, next.stderr.String())
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
	if n := len(api.Requests()) - before; n != 1 {
		t.Fatalf("%d weekly review requests, want 1", n)
	}
}

func TestDisabledAnthropicConnectionSkipsJobsAsUnconfigured(t *testing.T) {
	t.Parallel()
	d, api, _, before := startWeeklyReview(t)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM connections WHERE id = 'service/anthropic' AND enabled = 1"); n != 1 {
		t.Fatalf("%d enabled Anthropic connections, want 1", n)
	}
	asstExec(t, d, "UPDATE connections SET enabled = 0, state = 'unconfigured', reason = '' WHERE id = 'service/anthropic'")
	for i := range 2 {
		tick, stderr := tickProcess(t)
		if code := exitWithin(t, tick, 60*time.Second); code != cli.ExitOK {
			t.Fatalf("tick %d exit %d: %s", i, code, stderr)
		}
		if status, text := schedulerStatus(t, d, "weekly-review"); status != "skipped_unconfigured" {
			t.Fatalf("weekly review %s %s with the connection disabled, want skipped_unconfigured: %s", status, text, stderr)
		}
		if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE state = 'running'"); n != 0 {
			t.Fatalf("%d assistant runs left running with the connection disabled", n)
		}
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
	if n := len(api.Requests()) - before; n != 0 {
		t.Fatalf("%d model requests with the connection disabled", n)
	}
}

func TestConcurrentSettingsCommitsDoNotStallTheDrain(t *testing.T) {
	d, api, f, before := startWeeklyReview(t, asstReply("reviewed"))
	release := asstHold(t, api)
	_, logs := startServeLogged(t, f.dir)
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 30*time.Second, "the weekly review request held inside serve", func() bool { return len(api.Requests()) > before })

	type commit struct {
		run  appRun
		took time.Duration
	}
	set := func(key, value string, out chan<- commit) {
		start := time.Now()
		ran := runApp(t, "settings", "set", key, value)
		out <- commit{run: ran, took: time.Since(start)}
	}
	disable, other := make(chan commit, 1), make(chan commit, 1)
	go set(module.FlagKey(module.Assistant), "false", disable)
	go set("monthly_budget_usd", "50", other)
	limit := 10 * time.Second
	for name, ch := range map[string]chan commit{"disabling the assistant": disable, "the budget": other} {
		select {
		case c := <-ch:
			if c.run.code != cli.ExitOK {
				t.Fatalf("settings commit for %s: exit %d %s", name, c.run.code, c.run.stderr)
			}
			if c.took >= limit {
				t.Fatalf("settings commit for %s took %s", name, c.took)
			}
		case <-time.After(limit):
			t.Fatalf("settings commit for %s did not finish within %s", name, limit)
		}
	}
	if text := logs.String(); strings.Contains(text, "module work did not drain before the change") {
		t.Fatalf("the disabled snapshot was published before the weekly review stopped: %s", text)
	}
	waitUntil(t, 10*time.Second, "the drained weekly review to settle", schedulerTerminal(d, "weekly-review"))
	if status, _ := schedulerStatus(t, d, "weekly-review"); status != "skipped_module" {
		t.Fatalf("weekly review %s after disabling the assistant, want skipped_module", status)
	}
	release()
}

func TestServeStaysUpAndNotReadyUntilSchedulerRecoverySucceeds(t *testing.T) {
	d, api, f, before := startWeeklyReview(t, asstReply("abandoned"), asstReply("reviewed"))
	g, seen := holdJobRequests(t, api, 1)
	interruptTickDuringWeeklyReview(t, api, g, seen, d)

	canonical, err := db.CanonicalPath(filepath.Join(f.dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	locks := canonical + ".locks"
	if err := os.RemoveAll(locks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locks, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opsBase := opsAddr(t)
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	for name, value := range notify.Env() {
		setEnv(t, name, value)
	}

	s := serveCLIProcess(t, f.dir)
	waitUntil(t, 30*time.Second, "two failed scheduler recoveries", func() bool {
		return strings.Count(s.stderr.String(), "service scheduler failed; restarting in ") >= 2
	})
	notify.Wait("READY=1")
	status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/readyz", nil)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "scheduler recovery has not finished") {
		t.Fatalf("readyz while recovery fails = %d %s, want 503 naming the recovery", status, body)
	}
	if slices.Contains(notify.Messages(), "WATCHDOG=1") {
		t.Fatalf("serve fed the watchdog while its scheduler recovery failed: %q", notify.Messages())
	}

	if err := os.Remove(locks); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "readiness after the recovery", func() bool {
		status, _ := readiness(t, opsBase)
		return status == http.StatusOK
	})
	notify.Wait("WATCHDOG=1")
	waitUntil(t, 60*time.Second, "the recovered weekly review to settle", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s %s after the recovery, want ok: %s", status, text, s.stderr.String())
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
	if n := len(api.Requests()) - before; n != 2 {
		t.Fatalf("%d weekly review requests, want 2", n)
	}
	if code := s.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("serve exit %d: %s", code, s.stderr.String())
	}
}

func TestAgentTickLeavesAFullServeRecoveredJobRunAlone(t *testing.T) {
	t.Parallel()
	d, api, f, before := startWeeklyReview(t, asstReply("abandoned"), asstReply("reviewed in serve"))
	g, seen := holdJobRequests(t, api, 2)
	jobRun := interruptTickDuringWeeklyReview(t, api, g, seen, d)
	var thread int64
	if err := d.Read.QueryRow("SELECT thread_id FROM job_run_threads WHERE job_run_id = ?", jobRun).Scan(&thread); err != nil {
		t.Fatal(err)
	}

	s := serveCLIProcess(t, f.dir)
	waitUntil(t, 30*time.Second, "serve's held recovery", func() bool { return seen.Load() == 2 })
	ownRun := asstLastRun(t, d, thread)
	resumer := liveRun(t, d, ownRun)

	tick, stderr := tickProcess(t)
	if code := exitWithin(t, tick, 60*time.Second); code != cli.ExitOK && code != cli.ExitTempFail {
		t.Fatalf("tick exit %d: %s", code, stderr)
	}
	if n := len(api.Requests()) - before; n != 2 {
		t.Fatalf("%d model requests while serve resumes the job run, want 2", n)
	}
	if owner := liveRun(t, d, ownRun); owner != resumer {
		t.Fatalf("job run %d moved from %s to %s", ownRun, resumer, owner)
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "running" {
		t.Fatalf("weekly review %s %s while serve runs it, want running", status, text)
	}

	g.release(1)
	if state, text := asstSettle(t, d, ownRun); state != "done" {
		t.Fatalf("job run %s %s, want done in serve", state, text)
	}
	waitUntil(t, 30*time.Second, "the weekly review to settle", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly review %s %s, want ok: %s", status, text, s.stderr.String())
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Fatalf("%d weekly review occurrences, want 1", n)
	}
}

func TestAgentSchedulerAndTickStopCleanlyWhenCanceledDuringStartup(t *testing.T) {
	t.Parallel()
	_, _, f := moduleFixture(t, flagsExcept("", ""))
	for _, args := range [][]string{{"agent", "scheduler"}, {"agent", "tick"}} {
		for _, delay := range []time.Duration{0, time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 200 * time.Millisecond} {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan appRun, 1)
			go func() { done <- runAppContext(t, f, ctx, args...) }()
			time.Sleep(delay)
			cancel()
			select {
			case ran := <-done:
				if ran.code != cli.ExitOK || strings.Contains(ran.stderr, "error:") {
					t.Fatalf("%s canceled after %s: exit %d %s", strings.Join(args, " "), delay, ran.code, ran.stderr)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s ignored cancellation after %s", strings.Join(args, " "), delay)
			}
		}
	}
}
