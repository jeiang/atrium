package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

const weeklyRunning = "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = 'weekly-review' AND r.status = 'running'"

func schedulerRuns(t *testing.T, d *db.DB, name string) int {
	t.Helper()
	return asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ?", name)
}

func waitUntil(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestSchedulerLockIdentityFollowsTheCanonicalDatabase(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	api.Push(asstReply("reviewed"))
	release := asstHold(t, api)
	before := len(api.Requests())
	schedulerDue(t, d, "weekly-review")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 20*time.Second, "the held weekly review request", func() bool { return len(api.Requests()) > before })

	asstExec(t, d, "UPDATE jobs SET lease_until = 0, next_due = ? WHERE name = 'weekly-review'", float64(time.Now().Unix())-1)
	schedulerDue(t, d, "daily-nudges")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	other := cliExitProcessArgs(t, []string{"agent", "scheduler"})
	other.Env = append(other.Env, "ATRIUM_DB="+filepath.Join(link, "atrium.db"))
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "the second scheduler to run the free job", schedulerTerminal(d, "daily-nudges"))
	if err := other.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = other.Wait()

	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("weekly review has %d runs, want only the held one", n)
	}
	if n := asstCount(t, d, weeklyRunning); n != 1 {
		t.Errorf("weekly review running runs = %d, want the held one", n)
	}
	if got := len(api.Requests()) - before; got != 1 {
		t.Errorf("%d weekly review requests, want 1", got)
	}
	release()
	waitUntil(t, 30*time.Second, "the held weekly review to finish", schedulerTerminal(d, "weekly-review"))
	cancel()
	<-done
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Errorf("weekly review status = %s %s, want ok", status, text)
	}
}
