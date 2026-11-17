package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

const weeklyKey = "assistant/weekly-review"

type executorCLI struct {
	cmd    *exec.Cmd
	stdout *lockedBuffer
	stderr *lockedBuffer
	done   chan int
}

func startExecutorCLI(t *testing.T, dir string, args ...string) *executorCLI {
	t.Helper()
	cmd := cliExitProcessArgs(t, args)
	cmd.Env = append(cmd.Env, "ATRIUM_DB="+filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR="+filepath.Join(dir, "cache"))
	c := &executorCLI{cmd: cmd, stdout: &lockedBuffer{}, stderr: &lockedBuffer{}, done: make(chan int, 1)}
	cmd.Stdout, cmd.Stderr = c.stdout, c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { c.done <- exitCode(cmd.Wait()) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return c
}

func (c *executorCLI) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-c.done:
		return code
	case <-time.After(3 * time.Minute):
		t.Fatalf("%v did not finish: stdout %q stderr %q", c.cmd.Args, c.stdout.String(), c.stderr.String())
		return -1
	}
}

func runExecutorCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	c := startExecutorCLI(t, dir, args...)
	code := c.wait(t)
	return code, c.stdout.String(), c.stderr.String()
}

type runRecord struct {
	Run    int64  `json:"run"`
	Job    string `json:"job"`
	Status string `json:"status"`
	Owner  string `json:"owner"`
	Error  string `json:"error"`
}

func parseRunRecord(t *testing.T, stdout string) runRecord {
	t.Helper()
	var r runRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &r); err != nil {
		t.Fatalf("run record %q: %v", stdout, err)
	}
	return r
}

func servedHere(owner string) bool {
	return strings.HasPrefix(owner, "manual/") && strings.Contains(owner, fmt.Sprintf(":%d:", os.Getpid()))
}

func startExecutorServe(t *testing.T, f *fixture) {
	t.Helper()
	addrFile := filepath.Join(t.TempDir(), "serve-addr")
	setEnv(t, "ATRIUM_SERVE_ADDR_FILE", addrFile)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() {
		done <- runAppContext(t, f, ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitUntil(t, 60*time.Second, "serve to report its address", func() bool {
		raw, err := os.ReadFile(addrFile)
		return err == nil && strings.TrimSpace(string(raw)) != ""
	})
}

func weeklyFixture(t *testing.T, replies ...anthropictest.Response) (string, *db.DB, *modelLog, *fixture) {
	t.Helper()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	api.Push(replies...)
	return dir, d, api, f
}

func holdModel(t *testing.T, api *modelLog) func() {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	var first atomic.Bool
	logRequest := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		logRequest(r)
		if first.CompareAndSwap(false, true) {
			<-release
		}
	})
	return open
}

func TestJobRunExecutesInsideServe(t *testing.T) {
	t.Parallel()
	dir, d, _, f := weeklyFixture(t, asstReply("reviewed"))
	startExecutorServe(t, f)
	code, stdout, stderr := runExecutorCLI(t, dir, "job", "run", weeklyKey)
	if code != cli.ExitOK {
		t.Fatalf("job run exit %d: %s %s", code, stdout, stderr)
	}
	r := parseRunRecord(t, stdout)
	if r.Status != "ok" || r.Job != weeklyKey || !servedHere(r.Owner) {
		t.Fatalf("run record %+v, want ok and owned by serve pid %d", r, os.Getpid())
	}
	var owner, status string
	if err := d.Read.QueryRow("SELECT owner, status FROM job_runs WHERE id = ?", r.Run).Scan(&owner, &status); err != nil {
		t.Fatal(err)
	}
	if owner != r.Owner || status != "ok" {
		t.Errorf("job_runs row %d: owner %q status %q", r.Run, owner, status)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("weekly review has %d runs, want 1", n)
	}
}

func TestJobRunRefusesASecondRequest(t *testing.T) {
	t.Parallel()
	dir, d, api, f := weeklyFixture(t, asstReply("reviewed"), asstReply("reviewed"))
	release := holdModel(t, api)
	startExecutorServe(t, f)
	first := startExecutorCLI(t, dir, "job", "run", weeklyKey)
	second := startExecutorCLI(t, dir, "job", "run", weeklyKey)
	asstWaitCount(t, d, 1, weeklyRunning)
	var refused, ran *executorCLI
	for refused == nil {
		select {
		case code := <-first.done:
			first.done <- code
			refused, ran = first, second
		case code := <-second.done:
			second.done <- code
			refused, ran = second, first
		case <-time.After(2 * time.Minute):
			t.Fatalf("neither request was refused: %s / %s", first.stderr.String(), second.stderr.String())
		}
	}
	running := weeklyLeaseOf(t, d).run
	if code := refused.wait(t); code != cli.ExitTempFail || !strings.Contains(refused.stderr.String(), fmt.Sprintf("already running (run %d)", running)) {
		t.Fatalf("refused request exit %d stderr %q, want 75 naming run %d", code, refused.stderr.String(), running)
	}
	release()
	if code := ran.wait(t); code != cli.ExitOK {
		t.Fatalf("running request exit %d: %s", code, ran.stderr.String())
	}
	if r := parseRunRecord(t, ran.stdout.String()); r.Run != running || r.Status != "ok" {
		t.Errorf("run record %+v, want run %d ok", r, running)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("weekly review has %d runs, want 1", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE status IN ('requested', 'refused')"); n != 0 {
		t.Errorf("%d request rows remain", n)
	}
}

func TestJobRunCanceledWaitLeavesTheJobRunning(t *testing.T) {
	t.Parallel()
	dir, d, api, f := weeklyFixture(t, asstReply("reviewed"))
	release := holdModel(t, api)
	startExecutorServe(t, f)
	c := startExecutorCLI(t, dir, "job", "run", weeklyKey)
	asstWaitCount(t, d, 1, weeklyRunning)
	running := weeklyLeaseOf(t, d).run
	if err := c.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(t); code != cli.ExitInterrupt || !strings.Contains(c.stderr.String(), fmt.Sprintf("run %d of %s continues", running, weeklyKey)) {
		t.Fatalf("canceled wait exit %d stderr %q", code, c.stderr.String())
	}
	if n := asstCount(t, d, weeklyRunning); n != 1 {
		t.Fatalf("weekly review running runs = %d after the canceled wait, want 1", n)
	}
	release()
	waitUntil(t, 60*time.Second, "the weekly review to finish", schedulerTerminal(d, "weekly-review"))
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Errorf("weekly review status %s %s, want ok", status, text)
	}
}

func TestJobRunWithoutServeFails(t *testing.T) {
	t.Parallel()
	dir, d, _, _ := weeklyFixture(t)
	before := asstCount(t, d, "SELECT COUNT(*) FROM job_runs")
	code, stdout, stderr := runExecutorCLI(t, dir, "job", "run", weeklyKey)
	if code != cli.ExitUnavailable || !strings.Contains(stderr, "serve is not running") {
		t.Fatalf("job run without serve exit %d: %s %s", code, stdout, stderr)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs"); n != before {
		t.Errorf("job runs %d, want %d", n, before)
	}
}

func TestSyncWithServeRunningDelegates(t *testing.T) {
	t.Parallel()
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mrvMailFixture(t, "shadow")
	f := fixtureFor(t)
	d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	count := func(query string) int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	startExecutorServe(t, f)
	waitUntil(t, 60*time.Second, "the mail sync job row", func() bool {
		return count("SELECT COUNT(*) FROM jobs WHERE declaration_key = 'mail/sync'") == 1
	})
	runs := count("SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/sync'")
	var code int
	var stdout, stderr string
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		code, stdout, stderr = runExecutorCLI(t, m.dir, "sync")
		if code != cli.ExitTempFail || time.Now().After(deadline) {
			break
		}
	}
	if code != cli.ExitOK {
		t.Fatalf("sync with serve running exit %d: %s %s", code, stdout, stderr)
	}
	r := parseRunRecord(t, stdout)
	if r.Job != "mail/sync" || r.Status != "ok" || !servedHere(r.Owner) {
		t.Fatalf("sync run record %+v, want mail/sync ok owned by serve pid %d", r, os.Getpid())
	}
	if n := count("SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/sync'"); n != runs+1 {
		t.Errorf("mail sync runs %d, want %d", n, runs+1)
	}
	code, stdout, stderr = runExecutorCLI(t, m.dir, "sync", "--account", "icloud")
	if code != cli.ExitTempFail || !strings.Contains(stderr, "atrium job run") {
		t.Errorf("sync --account with serve running exit %d: %s %s", code, stdout, stderr)
	}
	code, stdout, stderr = runExecutorCLI(t, m.dir, "agent", "tick")
	if code != cli.ExitTempFail || !strings.Contains(stderr, "serve is running") {
		t.Errorf("agent tick with serve running exit %d: %s %s", code, stdout, stderr)
	}
	if n := count("SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/sync'"); n != runs+1 {
		t.Errorf("mail sync runs %d after refusals, want %d", n, runs+1)
	}
}

func TestServeRecoversAStaleMailRunWithoutRerunningIt(t *testing.T) {
	t.Parallel()
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	m := mrvMailFixture(t, "shadow")
	f := fixtureFor(t)
	d := mustConnect(t, filepath.Join(m.dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	schedulerSeed(t, f, d)
	now := float64(time.Now().Unix())
	owner := "example-host:999999:deadbeef/1"
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	asstExec(t, d, "INSERT INTO job_runs (job_id, due_at, started_at, heartbeat_at, owner, status) SELECT id, ?, ?, ?, ?, 'running' FROM jobs WHERE declaration_key = 'mail/sync'", now-3600, now-3600, now-3600, owner)
	stale := int64(asstCount(t, d, "SELECT MAX(id) FROM job_runs"))
	asstExec(t, d, "UPDATE jobs SET lease_owner = ?, lease_until = ?, last_run_id = ? WHERE declaration_key = 'mail/sync'", owner, now-3000, stale)
	runs := asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/sync'")
	startExecutorServe(t, f)
	waitUntil(t, 60*time.Second, "the stale mail run to be recovered", func() bool {
		return asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE id = ? AND status <> 'running'", stale) == 1
	})
	var status string
	if err := d.Read.QueryRow("SELECT status FROM job_runs WHERE id = ?", stale).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Errorf("stale mail run status %q, want error", status)
	}
	time.Sleep(2 * time.Second)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/sync'"); n != runs {
		t.Errorf("mail sync runs %d after recovery, want %d", n, runs)
	}
}
