package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
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

const weeklyClaim = "SELECT r.id, r.owner, r.heartbeat_at, r.started_at, j.lease_owner, j.lease_until FROM job_runs r JOIN jobs j ON j.id = r.job_id " +
	"WHERE j.name = 'weekly-review' ORDER BY r.id DESC LIMIT 1"

type weeklyLease struct {
	run                int64
	owner              *string
	heartbeat, started float64
	leaseOwner         *string
	leaseUntil         *float64
}

func weeklyLeaseOf(t *testing.T, d *db.DB) weeklyLease {
	t.Helper()
	var l weeklyLease
	if err := d.Read.QueryRow(weeklyClaim).Scan(&l.run, &l.owner, &l.heartbeat, &l.started, &l.leaseOwner, &l.leaseUntil); err != nil {
		t.Fatal(err)
	}
	return l
}

func paceRequests(api *modelLog, held int, each time.Duration) {
	var seen atomic.Int64
	prev := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		prev(r)
		if seen.Add(1) <= int64(held) {
			time.Sleep(each)
		}
	})
}

func taskListCalls(n int) []anthropictest.Response {
	var out []anthropictest.Response
	for i := range n {
		out = append(out, anthropictest.Reply("tool_use", anthropictest.ToolUse(fmt.Sprintf("call-%d", i), "task_list", map[string]any{"query": fmt.Sprintf("step %d", i)})))
	}
	return out
}

func unixNow() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func startWeeklyReview(t *testing.T, replies ...anthropictest.Response) (*db.DB, *modelLog, *fixture, int) {
	t.Helper()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	api.Push(replies...)
	before := len(api.Requests())
	schedulerDue(t, d, "weekly-review")
	return d, api, f, before
}

func TestSchedulerRenewsItsClaimWhileAJobRunsPastTheLease(t *testing.T) {
	t.Parallel()
	d, api, f, before := startWeeklyReview(t, append(taskListCalls(8), asstReply("reviewed"))...)
	paceRequests(api, 8, 13*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 20*time.Second, "the first weekly review request", func() bool { return len(api.Requests()) > before })

	time.Sleep(100 * time.Second)
	now := unixNow()
	l := weeklyLeaseOf(t, d)
	if l.owner == nil || l.leaseOwner == nil || *l.owner != *l.leaseOwner {
		status, text := schedulerStatus(t, d, "weekly-review")
		t.Fatalf("run owner %v, job lease owner %v (run %s %s), want the same claim", l.owner, l.leaseOwner, status, text)
	}
	if l.heartbeat < l.started+60 {
		t.Errorf("heartbeat %.1f s after the start, want renewals past the 90 s lease", l.heartbeat-l.started)
	}
	if l.leaseUntil == nil || *l.leaseUntil <= now {
		t.Errorf("job lease until %v, want it renewed past %f", l.leaseUntil, now)
	}
	held := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases WHERE job_run_id = ? AND owner = ? AND lease_until > ?", l.run, *l.owner, now)
	if total := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases"); held == 0 || held != total {
		t.Errorf("%d renewed resource leases of %d, want every lease renewed by the claim", held, total)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("weekly review has %d runs, want the held one", n)
	}
	if asstCount(t, d, weeklyRunning) != 1 {
		t.Fatal("the weekly review finished before the lease passed; the pacing did not hold it")
	}

	waitUntil(t, 60*time.Second, "the paced weekly review to finish", schedulerTerminal(d, "weekly-review"))
	cancel()
	<-done
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Errorf("weekly review status = %s %s, want ok", status, text)
	}
	if got := len(api.Requests()) - before; got != 9 {
		t.Errorf("%d weekly review requests, want the 9 paced steps of one run", got)
	}
	if l := weeklyLeaseOf(t, d); l.leaseOwner != nil || l.leaseUntil != nil {
		t.Errorf("job lease %v until %v after finishing, want cleared", l.leaseOwner, l.leaseUntil)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases"); n != 0 {
		t.Errorf("%d resource leases after finishing, want none", n)
	}
}

func TestSchedulerExpiredClaimantCannotClearItsSuccessor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		effect  bool
		replies []anthropictest.Response
	}{
		{
			name:   "renewal stops the job before its next effect",
			effect: true,
			replies: append(taskListCalls(2),
				anthropictest.Reply("tool_use", anthropictest.ToolUse("call-add", "task_add", map[string]any{"title": "Renew the passport"})),
				asstReply("done")),
		},
		{
			name:    "a finished job does not settle the successor's run",
			replies: []anthropictest.Response{asstReply("reviewed")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, api, f, before := startWeeklyReview(t, tc.replies...)
			release := func() {}
			if tc.effect {
				paceRequests(api, 2, 13*time.Second)
			} else {
				release = asstHold(t, api)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan appRun, 1)
			go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
			asstWaitCount(t, d, 1, weeklyRunning)
			waitUntil(t, 20*time.Second, "the held weekly review request", func() bool { return len(api.Requests()) > before })

			l := weeklyLeaseOf(t, d)
			beat, until := unixNow(), unixNow()+3600
			asstExec(t, d, "UPDATE jobs SET lease_owner = 'successor', lease_until = ? WHERE name = 'weekly-review'", until)
			asstExec(t, d, "UPDATE job_runs SET owner = 'successor', heartbeat_at = ? WHERE id = ?", beat, l.run)
			asstExec(t, d, "UPDATE job_resource_leases SET owner = 'successor', lease_until = ? WHERE job_run_id = ?", until, l.run)
			leases := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases WHERE job_run_id = ?", l.run)
			if tc.effect {
				time.Sleep(35 * time.Second)
			} else {
				release()
				waitUntil(t, 30*time.Second, "the held run to finish", func() bool {
					return asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id = (SELECT thread_id FROM job_runs WHERE id = ?) AND state = 'done'", l.run) == 1
				})
			}
			cancel()
			ran := <-done
			if ran.code != cli.ExitOK {
				t.Fatalf("scheduler exit %d: %s", ran.code, ran.stderr)
			}
			if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE id = ? AND status = 'running' AND owner = 'successor' AND heartbeat_at = ?", l.run, beat); n != 1 {
				t.Errorf("the successor's run was changed by the stale claimant")
			}
			if n := asstCount(t, d, "SELECT COUNT(*) FROM jobs WHERE name = 'weekly-review' AND lease_owner = 'successor' AND lease_until = ?", until); n != 1 {
				t.Errorf("the successor's job lease was changed by the stale claimant")
			}
			if n := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases WHERE job_run_id = ? AND owner = 'successor' AND lease_until = ?", l.run, until); n != leases || n == 0 {
				t.Errorf("%d successor resource leases remain, want all %d", n, leases)
			}
			if n := asstCount(t, d, "SELECT COUNT(*) FROM tasks WHERE title = 'Renew the passport'"); n != 0 {
				t.Errorf("the stale claimant added the task %d times, want never", n)
			}
			if got, want := len(api.Requests())-before, map[bool]int{true: 2, false: 1}[tc.effect]; got > want {
				t.Errorf("%d weekly review requests, want at most %d before the claim was lost", got, want)
			}
		})
	}
}

func TestSchedulerResumesAnInterruptedJobRunUnderANewClaim(t *testing.T) {
	t.Parallel()
	d, api, f, before := startWeeklyReview(t, asstReply("reviewed"), asstReply("reviewed"))
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFirst)
	var first atomic.Bool
	logRequest := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		logRequest(r)
		if first.CompareAndSwap(false, true) {
			<-release
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	asstWaitCount(t, d, 1, weeklyRunning)
	waitUntil(t, 20*time.Second, "the held weekly review request", func() bool { return len(api.Requests()) > before })
	held := weeklyLeaseOf(t, d)
	cancel()
	if ran := <-done; ran.code != cli.ExitOK {
		t.Fatalf("first scheduler exit %d: %s", ran.code, ran.stderr)
	}
	l := weeklyLeaseOf(t, d)
	if l.run != held.run || l.owner == nil || *l.owner != *held.owner || l.leaseUntil == nil || *l.leaseUntil != 0 {
		t.Errorf("after the signal: run %d owner %v lease until %v, want run %d still owned by %s with its lease released", l.run, l.owner, l.leaseUntil, held.run, *held.owner)
	}
	if n := asstCount(t, d, weeklyRunning); n != 1 {
		t.Errorf("weekly review running runs = %d, want the interrupted one", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases"); n != 0 {
		t.Errorf("%d resource leases after the signal, want none", n)
	}
	releaseFirst()
	asstSet(t, "assistant_enabled", "false")
	parked := serveCLIProcess(t, f.dir)
	waitUntil(t, 30*time.Second, "the interrupted job to wait for its module", func() bool {
		return asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE id = ? AND status = 'skipped_module' AND ended_at IS NULL", held.run) == 1
	})
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id IN (SELECT thread_id FROM job_run_threads WHERE job_run_id = ?) AND state = 'interrupted'", held.run); n != 1 {
		t.Fatalf("%d interrupted assistant runs while disabled, want 1", n)
	}
	if got := len(api.Requests()) - before; got != 1 {
		t.Fatalf("%d requests while disabled, want only the initial interrupted request", got)
	}
	if code := parked.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Fatalf("disabled serve exit %d: %s", code, parked.stderr.String())
	}
	asstSet(t, "assistant_enabled", "true")

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan appRun, 1)
	go func() { done2 <- runAppContext(t, f, ctx2, "agent", "scheduler") }()
	waitUntil(t, 60*time.Second, "the resumed weekly review to finish", func() bool {
		return asstCount(t, d, "SELECT COUNT(*) FROM job_runs WHERE id = ? AND ended_at IS NOT NULL", held.run) == 1
	})
	cancel2()
	<-done2
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Errorf("weekly review status = %s %s, want ok", status, text)
	}
	if n := schedulerRuns(t, d, "weekly-review"); n != 1 {
		t.Errorf("weekly review has %d runs, want the resumed one", n)
	}
	l = weeklyLeaseOf(t, d)
	if l.owner == nil || *l.owner == *held.owner {
		t.Errorf("settled by owner %v, want a new claim fencing %s", l.owner, *held.owner)
	}
	if l.leaseOwner != nil || l.leaseUntil != nil {
		t.Errorf("job lease %v until %v, want cleared", l.leaseOwner, l.leaseUntil)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_resource_leases"); n != 0 {
		t.Errorf("%d resource leases after settling, want none", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id IN (SELECT thread_id FROM job_run_threads WHERE job_run_id = ?) AND parent_run_id IS NULL", held.run); n != 1 {
		t.Errorf("%d top-level runs in the job thread, want the one resumed run", n)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id IN (SELECT thread_id FROM job_run_threads WHERE job_run_id = ?) AND state = 'done'", held.run); n != 1 {
		t.Errorf("%d finished runs in the job thread, want 1", n)
	}
	if got := len(api.Requests()) - before; got != 2 {
		t.Errorf("%d weekly review requests, want the interrupted one and its resume", got)
	}
}
