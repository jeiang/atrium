package main

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func tickProcess(t *testing.T) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := cliExitProcessArgs(t, []string{"agent", "tick"})
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, stderr
}

func TestAgentTickWaitsAndExcludesConcurrentTicksPastTheLease(t *testing.T) {
	t.Parallel()
	d, api, _, before := startWeeklyReview(t, append(taskListCalls(8), asstReply("reviewed"))...)
	paceRequests(api, 8, 13*time.Second)
	first, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "first tick request", func() bool { return len(api.Requests()) > before })
	time.Sleep(100 * time.Second)
	lease := weeklyLeaseOf(t, d)
	if lease.heartbeat < lease.started+60 {
		t.Fatal("tick did not renew its claim")
	}
	other, otherErr := tickProcess(t)
	if code := exitCode(other.Wait()); code != 0 {
		t.Fatalf("busy tick exit %d: %s", code, otherErr)
	}
	if code := exitCode(first.Wait()); code != 0 {
		t.Fatalf("first tick exit %d: %s", code, stderr)
	}
	if schedulerRuns(t, d, "weekly-review") != 1 || len(api.Requests())-before != 9 {
		t.Fatal("ticks duplicated the occurrence")
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("%s: %s", status, text)
	}
}

func TestAgentTickSIGTERMPreservesItsRunForTheNextTick(t *testing.T) {
	t.Parallel()
	d, api, _, before := startWeeklyReview(t, asstReply("reviewed"))
	release := asstHold(t, api)
	first, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "held tick request", func() bool { return len(api.Requests()) > before })
	original := weeklyLeaseOf(t, d)
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	release()
	if asstCount(t, d, weeklyRunning) != 1 {
		t.Fatalf("cancelled tick lost recoverable run: %s", stderr)
	}
	api.Push(asstReply("resumed"))
	next, nextErr := tickProcess(t)
	if code := exitCode(next.Wait()); code != 0 {
		t.Fatalf("recovery exit %d: %s", code, nextErr)
	}
	if schedulerRuns(t, d, "weekly-review") != 1 || weeklyLeaseOf(t, d).run != original.run {
		t.Fatal("recovery made a new occurrence")
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("%s: %s", status, text)
	}
}

func TestAgentTickStoppedPastItsLeaseIsFencedBeforeSuccessor(t *testing.T) {
	t.Parallel()
	d, api, _, before := startWeeklyReview(t, append(taskListCalls(1), asstReply("reviewed"))...)
	release := asstHold(t, api)
	first, stderr := tickProcess(t)
	waitUntil(t, 30*time.Second, "held tick request", func() bool { return len(api.Requests()) > before })
	original := weeklyLeaseOf(t, d)
	if err := first.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Process.Signal(syscall.SIGCONT) })
	time.Sleep(time.Until(time.UnixMicro(int64(*original.leaseUntil * 1e6)).Add(2 * time.Second)))
	busy, _ := tickProcess(t)
	if code := exitCode(busy.Wait()); code != cli.ExitTempFail {
		t.Fatalf("stopped owner lock not held: exit %d", code)
	}
	if err := first.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	release()
	_ = first.Wait()
	if asstCount(t, d, weeklyRunning) != 1 {
		t.Fatalf("expired owner settled its run: %s", stderr)
	}
	api.Push(asstReply("successor"))
	next, nextErr := tickProcess(t)
	if code := exitCode(next.Wait()); code != 0 {
		t.Fatalf("successor exit %d: %s", code, nextErr)
	}
	if schedulerRuns(t, d, "weekly-review") != 1 {
		t.Fatal("successor duplicated occurrence")
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("%s: %s", status, text)
	}
}

func TestAgentTickTightensAnExistingLockDirectory(t *testing.T) {
	t.Parallel()
	d, _, f, _ := startWeeklyReview(t, asstReply("reviewed"))
	locks := f.dir + "/atrium.db.locks"
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	tick, stderr := tickProcess(t)
	if code := exitCode(tick.Wait()); code != 0 {
		t.Fatalf("tick exit %d: %s", code, stderr)
	}
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly status %s %s, want ok", status, text)
	}
	info, err := os.Stat(locks)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("lock directory mode %o, want 700", mode)
	}
}
