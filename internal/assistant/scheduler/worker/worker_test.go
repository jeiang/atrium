package worker_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const tz = "America/Port_of_Spain"

func local(y int, mo time.Month, d, h, mi int) float64 {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(y, mo, d, h, mi, 0, 0, loc).Unix())
}

var now = local(2026, 10, 15, 12, 0)

type world struct {
	t    *testing.T
	d    *db.DB
	path string
	cfg  config.Config
	env  config.Env
	srv  *anthropictest.Server
	svc  *service.Service
	w    *worker.Worker
}

func newWorld(t *testing.T, extra map[string]string, callables map[string]worker.Callable) *world {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"ATRIUM_DB": path, "ATRIUM_CACHE_DIR": filepath.Join(filepath.Dir(path), "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": tz}
	for k, v := range extra {
		vars[k] = v
	}
	env := config.Map(vars)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	srv := anthropictest.New(t)
	svc := service.New(t.Context(), d, cfg, env, &loop.Runner{Registry: registry.New()})
	svc.Client = srv.Client()
	t.Cleanup(svc.Close)
	w := worker.New(svc, callables)
	w.Owner = "w1"
	w.Spawn = func(_ string, fn func()) { fn() }
	return &world{t: t, d: d, path: path, cfg: cfg, env: env, srv: srv, svc: svc, w: w}
}

func (w *world) clock(at float64) {
	t := at
	w.svc.Now = func() float64 {
		t += 0.001
		return t
	}
}

func (w *world) job(n scheduler.NewJob) int64 {
	w.t.Helper()
	if n.Name == "" {
		n.Name = "brief"
	}
	if n.Prompt == "" {
		n.Prompt = "Check the inbox."
	}
	if n.Schedule == "" {
		n.Schedule = "daily 07:00"
	}
	if n.TZ == "" {
		n.TZ = tz
	}
	if n.Now == 0 {
		n.Now = now - 86400*4
	}
	var id int64
	err := w.d.WithTx(w.t.Context(), func(tx *sql.Tx) error {
		var err error
		id, err = scheduler.CreateJob(w.t.Context(), tx, n)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) loc() *time.Location {
	loc, err := config.Location(w.env)
	if err != nil {
		w.t.Fatal(err)
	}
	return loc
}

func (w *world) claim(at float64, owner string) ([]scheduler.Claim, []scheduler.Skipped) {
	w.t.Helper()
	return claimOn(w.t, w.d, w.cfg, w.loc(), at, owner)
}

func claimOn(t *testing.T, d *db.DB, cfg config.Config, loc *time.Location, at float64, owner string) ([]scheduler.Claim, []scheduler.Skipped) {
	t.Helper()
	c, s, err := scheduler.ClaimDue(t.Context(), d, cfg, loc, at, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func (w *world) getJob(id int64) *scheduler.Job {
	w.t.Helper()
	j, err := scheduler.GetJob(w.t.Context(), w.d.Read, id)
	if err != nil || j == nil {
		w.t.Fatalf("GetJob(%d) = %v, %v", id, j, err)
	}
	return j
}

type jobRun struct {
	ID       int64
	ThreadID *int64
	Status   string
	Error    *string
	Ended    *float64
}

func (w *world) jobRuns() []jobRun {
	w.t.Helper()
	rows, err := w.d.Read.QueryContext(w.t.Context(), "SELECT id, thread_id, status, error, ended_at FROM job_runs ORDER BY id")
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (jobRun, error) {
		var j jobRun
		return j, r.Scan(&j.ID, &j.ThreadID, &j.Status, &j.Error, &j.Ended)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) scalar(query string, args ...any) (v any) {
	w.t.Helper()
	if err := w.d.Read.QueryRowContext(w.t.Context(), query, args...).Scan(&v); err != nil {
		w.t.Fatal(err)
	}
	return v
}

func ok(text string) anthropictest.Response {
	return anthropictest.Reply("end_turn", anthropictest.Text(text))
}

func TestMissedRunsCoalesceToOne(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	if due := *w.getJob(id).NextDue; due != local(2026, 10, 12, 7, 0) {
		t.Fatalf("next_due = %v", due)
	}
	claims, skipped := w.claim(now, "w1")
	if len(claims) != 1 || len(skipped) != 0 || !claims[0].Missed || claims[0].DueAt != local(2026, 10, 12, 7, 0) {
		t.Fatalf("claims = %+v skipped = %+v", claims, skipped)
	}
	if next := *w.getJob(id).NextDue; next != local(2026, 10, 16, 7, 0) {
		t.Errorf("next_due = %v", next)
	}
	if again, _ := w.claim(now+60, "w1"); len(again) != 0 {
		t.Fatalf("second claim = %+v", again)
	}
	w.srv.Push(ok("done"))
	w.clock(now)
	if status := w.w.ExecuteClaim(t.Context(), claims[0]); status != scheduler.StatusMissed {
		t.Fatalf("status = %q", status)
	}
	runs := w.jobRuns()
	if len(runs) != 1 || runs[0].Status != scheduler.StatusMissed || runs[0].ThreadID == nil {
		t.Fatalf("job_runs = %+v", runs)
	}
}

func TestOnTimeRunIsOKAndUsesCronThread(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{Schedule: "daily 12:30", Now: now})
	w.srv.Push(ok("all quiet"))
	at := local(2026, 10, 15, 12, 30) + 5
	w.clock(at)
	claims, _, err := w.w.Tick(t.Context())
	if err != nil || len(claims) != 1 || claims[0].Job.ID != id {
		t.Fatalf("Tick = %+v, %v", claims, err)
	}
	runs := w.jobRuns()
	if len(runs) != 1 || runs[0].Status != scheduler.StatusOK {
		t.Fatalf("job_runs = %+v", runs)
	}
	thread, err := threads.Get(t.Context(), w.d.Read, *runs[0].ThreadID)
	if err != nil || thread.Kind != "cron" || thread.Effort != "high" || *thread.Title != "brief" {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	if got := w.getJob(id); got.LeaseOwner != nil {
		t.Errorf("lease_owner = %v", *got.LeaseOwner)
	}
	listed, err := scheduler.ListJobs(t.Context(), w.d.Read, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range listed {
		if j.ID == id && (j.LastStatus == nil || *j.LastStatus != "ok" || *j.LastThreadID != thread.ID) {
			t.Errorf("listed = %+v", j)
		}
	}
}

func TestOnlyOperatorWrittenPromptsAllowlistURLs(t *testing.T) {
	const prompt = "Read https://docs.test/brief and summarize it."
	due := local(2026, 10, 15, 12, 30)
	for _, c := range []struct {
		name     string
		promptBy string
		want     []string
	}{{"operator", scheduler.Operator, []string{"https://docs.test/brief"}}, {"agent", scheduler.Agent, nil}, {"reminder", "", nil}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, nil, nil)
			id := w.job(scheduler.NewJob{Name: c.name, Prompt: prompt, Schedule: "2026-10-15T12:30", Now: now, PromptBy: c.promptBy})
			w.srv.Push(ok("ok"))
			w.clock(due + 5)
			if claims, _, err := w.w.Tick(t.Context()); err != nil || len(claims) != 1 || claims[0].Job.ID != id {
				t.Fatalf("Tick = %+v, %v", claims, err)
			}
			thread, err := threads.Get(t.Context(), w.d.Read, *w.jobRuns()[0].ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(thread.AllowedURLs, ",") != strings.Join(c.want, ",") {
				t.Errorf("allowed urls = %v, want %v", thread.AllowedURLs, c.want)
			}
		})
	}
}

func TestOnceJobFiresOnceThenDisables(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{Name: "ping", Prompt: "p", Schedule: "2026-10-15T12:30", Now: now})
	claims, _ := w.claim(local(2026, 10, 15, 12, 31), "w1")
	if len(claims) != 1 || claims[0].Missed {
		t.Fatalf("claims = %+v", claims)
	}
	if j := w.getJob(id); j.Enabled || j.NextDue != nil {
		t.Errorf("job = %+v", j)
	}
}

func TestLeasePreventsDoubleClaim(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	if first, _ := w.claim(now, "w1"); len(first) != 1 {
		t.Fatalf("first = %+v", first)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE jobs SET next_due = ? WHERE id = ?", now-10, id); err != nil {
		t.Fatal(err)
	}
	if second, _ := w.claim(now+1, "w2"); len(second) != 0 {
		t.Fatalf("a live lease allowed %+v", second)
	}
	if third, _ := w.claim(now+scheduler.LeaseSeconds+1, "w2"); len(third) == 0 {
		t.Fatal("a lapsed lease blocked the claim")
	}
}

func TestTwoConnectionsCannotBothClaim(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.job(scheduler.NewJob{})
	other, err := db.Connect(w.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, d := range []*db.DB{w.d, other} {
		wg.Go(func() {
			c, _, err := scheduler.ClaimDue(t.Context(), d, w.cfg, w.loc(), now, "w"+string(rune('1'+i)), nil)
			if err != nil {
				t.Error(err)
			}
			counts[i] = len(c)
		})
	}
	wg.Wait()
	if counts[0]+counts[1] != 1 {
		t.Fatalf("claims = %v", counts)
	}
}

func TestPauseSkips(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	if err := os.WriteFile(w.cfg.PausePath(), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	claims, skipped := w.claim(now, "w1")
	if len(claims) != 0 || len(skipped) != 1 || skipped[0].Status != scheduler.StatusSkippedPause {
		t.Fatalf("claims = %+v skipped = %+v", claims, skipped)
	}
	job := w.getJob(id)
	if *job.NextDue != local(2026, 10, 16, 7, 0) || job.LeaseOwner != nil {
		t.Errorf("job = %+v", job)
	}
	if runs := w.jobRuns(); len(runs) != 1 || runs[0].Status != "skipped_pause" || runs[0].ThreadID != nil {
		t.Errorf("job_runs = %+v", runs)
	}
}

func TestBudgetSkipNotifiesOnceAndMatchesGolden(t *testing.T) {
	ntfy := golden.Replay(t, golden.LoadScenario(t, "ntfy", "budget_skip").Exchanges)
	w := newWorld(t, map[string]string{"NTFY_URL": ntfy.URL, "NTFY_TOPIC": "atrium"}, nil)
	id := w.job(scheduler.NewJob{})
	if err := budget.SetSetting(t.Context(), w.d.Write, budget.BudgetKey, 0); err != nil {
		t.Fatal(err)
	}
	w.svc.Now = func() float64 { return now }
	claims, skipped, err := w.w.Tick(t.Context())
	if err != nil || len(claims) != 0 || len(skipped) != 1 {
		t.Fatalf("Tick = %+v %+v %v", claims, skipped, err)
	}
	if got := w.scalar("SELECT status FROM job_runs"); got != "skipped_budget" {
		t.Fatalf("status = %v", got)
	}
	if got := w.scalar("SELECT title FROM notifications"); got != "Skipped: brief" {
		t.Fatalf("title = %v", got)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE jobs SET next_due = ? WHERE id = ?", now-10, id); err != nil {
		t.Fatal(err)
	}
	w.svc.Now = func() float64 { return now + 1 }
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.scalar("SELECT COUNT(*) FROM job_runs WHERE job_id = ?", id); got != int64(2) {
		t.Errorf("job_runs = %v", got)
	}
	if got := w.scalar("SELECT COUNT(*) FROM notifications"); got != int64(1) {
		t.Errorf("notifications = %v", got)
	}
}

func TestNightlyIsSeededAndRunsAsNightly(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.svc.Now = func() float64 { return local(2026, 10, 15, 12, 0) }
	w.w.Spawn = func(string, func()) {}
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var id int64
	var schedule string
	var due float64
	if err := w.d.Read.QueryRowContext(t.Context(), "SELECT id, schedule, next_due FROM jobs WHERE kind = 'nightly'").Scan(&id, &schedule, &due); err != nil {
		t.Fatal(err)
	}
	if schedule != "daily 02:30" || due != local(2026, 10, 16, 2, 30) {
		t.Fatalf("nightly job = %q %v", schedule, due)
	}
	w.w.Spawn = func(_ string, fn func()) { fn() }
	w.srv.Push(ok("tidy"))
	w.clock(local(2026, 10, 16, 2, 31))
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var threadID int64
	if err := w.d.Read.QueryRowContext(t.Context(), "SELECT thread_id FROM job_runs WHERE job_id = ?", id).Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	thread, err := threads.Get(t.Context(), w.d.Read, threadID)
	if err != nil || thread.Kind != "nightly" {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	msgs, err := journal.MessagesOf(t.Context(), w.d.Read, thread.ID, false)
	if err != nil || len(msgs) == 0 || !strings.Contains(string(msgs[0].Content[0]), nightly.Prompt[:40]) {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	err = w.d.WithTx(t.Context(), func(tx *sql.Tx) error { return scheduler.EnsureNightly(t.Context(), tx, tz, now) })
	if err != nil || w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'") != int64(1) {
		t.Fatalf("EnsureNightly seeded twice: %v", err)
	}
}

func TestBuiltinsSeedOnceAndStayDisabled(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.svc.Now = func() float64 { return now }
	w.w.Spawn = func(string, func()) {}
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'builtin'"); got != int64(len(scheduler.BuiltinJobs)) {
		t.Fatalf("builtins = %v", got)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE jobs SET enabled = 0 WHERE name = 'daily-nudges'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'builtin'") != int64(len(scheduler.BuiltinJobs)) ||
		w.scalar("SELECT enabled FROM jobs WHERE name = 'daily-nudges'") != int64(0) {
		t.Error("a built-in job was reseeded or re-enabled")
	}
}

func TestCallableJobs(t *testing.T) {
	calls := 0
	w := newWorld(t, nil, map[string]worker.Callable{
		scheduler.FXRatesName: func(context.Context, worker.Env) (worker.Result, error) { calls++; return worker.Result{}, nil },
		scheduler.MediaSweepName: func(context.Context, worker.Env) (worker.Result, error) {
			return worker.Result{}, budget.BudgetExceeded("monthly budget: spent")
		},
		scheduler.MediaChannelsName: func(context.Context, worker.Env) (worker.Result, error) { panic("boom") },
	})
	for _, name := range []string{scheduler.FXRatesName, scheduler.MediaSweepName, scheduler.MediaChannelsName, scheduler.MediaThumbnailsName} {
		w.job(scheduler.NewJob{Name: name, Prompt: "x", Schedule: "daily 07:00", Kind: scheduler.JobBuiltin, SkipLimits: true})
	}
	claims, _ := w.claim(now, "w1")
	if len(claims) != 4 {
		t.Fatalf("claims = %d", len(claims))
	}
	w.clock(now)
	got := map[string]string{}
	for _, c := range claims {
		got[c.Job.Name] = w.w.ExecuteClaim(t.Context(), c)
	}
	want := map[string]string{
		scheduler.FXRatesName: scheduler.StatusMissed, scheduler.MediaSweepName: scheduler.StatusSkippedBudget,
		scheduler.MediaChannelsName: scheduler.StatusError, scheduler.MediaThumbnailsName: scheduler.StatusError,
	}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("%s: status %q, want %q", name, got[name], status)
		}
	}
	if calls != 1 || len(w.srv.Requests()) != 0 {
		t.Errorf("calls = %d, model requests = %d", calls, len(w.srv.Requests()))
	}
	for _, r := range w.jobRuns() {
		if r.Status == scheduler.StatusError && (r.Error == nil || *r.Error == "") {
			t.Errorf("error run without text: %+v", r)
		}
	}
	if w.scalar("SELECT COUNT(*) FROM jobs WHERE lease_until IS NOT NULL") != int64(0) {
		t.Error("a lease outlived its job")
	}
}

func TestInterruptedClaimIsNeverFailed(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	claims, _ := w.claim(now, "w1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if status := w.w.ExecuteClaim(ctx, claims[0]); status != "" {
		t.Fatalf("status = %q", status)
	}
	runs := w.jobRuns()
	if len(runs) != 1 || runs[0].Status != scheduler.StatusRunning || runs[0].Error != nil {
		t.Fatalf("job_runs = %+v", runs)
	}
	if w.getJob(id).LeaseUntil == nil {
		t.Error("the lease was released")
	}
}

func TestSettleResumedFollowsTheFinishedRun(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	claims, _ := w.claim(now, "w1")
	w.clock(now)
	thread, err := service.CreateThread(t.Context(), w.d.Write, now, threads.NewThread{Kind: threads.Cron, JobID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE job_runs SET thread_id = ? WHERE id = ?", thread, claims[0].JobRunID); err != nil {
		t.Fatal(err)
	}
	run, err := w.svc.Prepare(t.Context(), thread, "check", now, service.Start{Kind: threads.Cron})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SettleResumed(t.Context(), w.d, now+1); err != nil {
		t.Fatal(err)
	}
	if got := w.jobRuns()[0].Status; got != scheduler.StatusRunning {
		t.Fatalf("a running agent run settled its job run as %q", got)
	}
	if err := journal.FinishRun(t.Context(), w.d.Write, run, journal.Done, now+2, nil); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SettleResumed(t.Context(), w.d, now+3); err != nil {
		t.Fatal(err)
	}
	if got := w.jobRuns()[0]; got.Status != scheduler.StatusMissed || got.Error != nil {
		t.Fatalf("job run = %+v", got)
	}
	if w.getJob(id).LeaseUntil != nil {
		t.Error("the lease was kept")
	}
}

func TestReleaseStaleMarksWorkerLost(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	w.claim(now, "w1")
	if err := scheduler.ReleaseStale(t.Context(), w.d, now+1); err != nil {
		t.Fatal(err)
	}
	if got := w.jobRuns()[0]; got.Status != scheduler.StatusRunning {
		t.Fatalf("a live lease was released: %+v", got)
	}
	if err := scheduler.ReleaseStale(t.Context(), w.d, now+scheduler.LeaseSeconds+1); err != nil {
		t.Fatal(err)
	}
	got := w.jobRuns()[0]
	if got.Status != scheduler.StatusError || got.Error == nil || *got.Error != "worker lost" || w.getJob(id).LeaseOwner != nil {
		t.Fatalf("job run = %+v", got)
	}
}

func TestCycleResumesLapsedRunsOnce(t *testing.T) {
	w := newWorld(t, nil, nil)
	stale := float64(time.Now().Unix()) - journal.LeaseSeconds - 10
	thread, err := service.CreateThread(t.Context(), w.d.Write, stale, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.svc.Prepare(t.Context(), thread, "find", stale, service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Now = nil
	w.srv.Push(ok("found"))
	w.w.Spawn = func(string, func()) {}
	for range 3 {
		if err := w.w.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		w.svc.Wait()
	}
	found, err := journal.GetRun(t.Context(), w.d.Read, run)
	if err != nil || found.State != journal.Done || len(w.srv.Requests()) != 1 {
		t.Fatalf("run = %+v requests = %d, %v", found, len(w.srv.Requests()), err)
	}
}

func TestCheckpointTruncatesWALWhenIdle(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.job(scheduler.NewJob{Schedule: "daily 12:30", Now: now})
	w.srv.Push(ok("quiet"))
	w.clock(local(2026, 10, 15, 12, 30) + 5)
	if _, _, err := w.w.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(w.path + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("wal after the run: %v, %v", info, err)
	}
}

func TestRunStopsOnCancelAndWaits(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.w.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.w.Run(ctx)
		close(done)
	}()
	for deadline := time.Now().Add(30 * time.Second); w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'") != int64(1) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	if got := w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'"); got != int64(1) {
		t.Errorf("nightly jobs = %v", got)
	}
}

func TestUnparsableScheduleIsSkippedNotPoisoning(t *testing.T) {
	w := newWorld(t, nil, nil)
	bad := w.job(scheduler.NewJob{Name: "bad"})
	good := w.job(scheduler.NewJob{Name: "good"})
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE jobs SET schedule = 'nonsense' WHERE id = ?", bad); err != nil {
		t.Fatal(err)
	}
	claims, _ := w.claim(now, "w1")
	if len(claims) != 1 || claims[0].Job.ID != good {
		t.Fatalf("claims = %+v", claims)
	}
	if j := w.getJob(bad); j.LeaseOwner != nil || j.LastRunID != nil || *j.NextDue != local(2026, 10, 12, 7, 0) || !j.Enabled {
		t.Errorf("the unparsable job was touched: %+v", j)
	}
}

func TestInterruptedJobRunSettlesAfterResumeNotWorkerLost(t *testing.T) {
	w := newWorld(t, nil, nil)
	id := w.job(scheduler.NewJob{})
	claims, _ := w.claim(now, "w1")
	w.svc.Now = func() float64 { return now }
	thread, err := service.CreateThread(t.Context(), w.d.Write, now, threads.NewThread{Kind: threads.Cron, JobID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Write.ExecContext(t.Context(), "UPDATE job_runs SET thread_id = ? WHERE id = ?", thread, claims[0].JobRunID); err != nil {
		t.Fatal(err)
	}
	run, err := w.svc.Prepare(t.Context(), thread, "check", now, service.Start{Kind: threads.Cron})
	if err != nil {
		t.Fatal(err)
	}
	w.w.Spawn = func(string, func()) {}
	w.srv.Push(ok("done"))
	later := now + scheduler.LeaseSeconds + journal.LeaseSeconds + 10
	w.svc.Now = func() float64 { return later }
	if err := w.w.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if got := w.jobRuns()[0]; got.Status == scheduler.StatusError {
		t.Fatalf("the interrupted job run was released: %+v", got)
	}
	if err := w.w.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	found, err := journal.GetRun(t.Context(), w.d.Read, run)
	if err != nil || found.State != journal.Done {
		t.Fatalf("run = %+v, %v", found, err)
	}
	if got := w.jobRuns()[0]; got.Status != scheduler.StatusMissed || got.Error != nil {
		t.Fatalf("job run = %+v", got)
	}
	if w.getJob(id).LeaseOwner != nil {
		t.Error("the lease was kept")
	}
}

func TestCycleSkipsBuiltinsWithoutFunctionsWithoutRows(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.svc.Now = func() float64 { return now }
	w.srv.Push(ok("a"), ok("b"), ok("c"), ok("d"), ok("e"), ok("f"))
	for range 3 {
		if err := w.w.Cycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	later := now + 7*86400
	w.svc.Now = func() float64 { return later }
	if err := w.w.Cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.jobRuns() {
		var name string
		_ = w.d.Read.QueryRow("SELECT j.name FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE r.id = ?", r.ID).Scan(&name)
		for _, c := range scheduler.CallableNames {
			if name == c {
				t.Errorf("job run recorded for %s: %+v", name, r)
			}
		}
	}
	if got := w.scalar("SELECT COUNT(*) FROM jobs WHERE kind = 'builtin' AND lease_until IS NOT NULL"); got != int64(0) {
		t.Errorf("leased builtins = %v", got)
	}
}
