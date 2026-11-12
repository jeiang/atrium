package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

type Recovery func(context.Context, Run, []int64) (Result, error)

type Gate struct {
	Paused     bool
	OverBudget bool
}

type Dependencies struct {
	DBPath  string
	Now     func() float64
	Zone    func(context.Context) string
	Owner   string
	Gate    func(context.Context, float64) (Gate, error)
	Recover Recovery
	Resume  func(context.Context) (int, error)
	Idle    func(context.Context) bool
	Notice  func(context.Context, Skipped, float64) error
	RunCron func(context.Context, Run) (Result, error)
	Enabled func(string) bool
	Beat    func()
}

type Scheduler struct {
	db       *db.DB
	jobs     []Definition
	deps     Dependencies
	interval time.Duration

	wg      sync.WaitGroup
	active  atomic.Int64
	dirty   atomic.Bool
	claims  atomic.Int64
	busy    atomic.Int64
	started atomic.Int64
}

func New(d *db.DB, jobs []Definition, deps Dependencies) *Scheduler {
	return &Scheduler{db: d, jobs: jobs, deps: deps, interval: time.Duration(TuningAgentSchedulerTickSeconds) * time.Second}
}

func (s *Scheduler) now() float64 {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return float64(time.Now().UnixMicro()) / 1e6
}

func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func (s *Scheduler) admit(gate Gate) Admit {
	return func(job Job) Status {
		switch {
		case gate.Paused:
			return StatusSkippedPause
		case gate.OverBudget && SpendOf(s.jobs, job) == AlwaysSpends:
			return StatusSkippedBudget
		}
		return ""
	}
}

func (s *Scheduler) owner() string {
	return fmt.Sprintf("%s/%d", s.deps.Owner, s.claims.Add(1))
}

func (s *Scheduler) Tick(ctx context.Context) error {
	stamp := s.now()
	tz := s.deps.Zone(ctx)
	if err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return Ensure(ctx, tx, s.jobs, tz, stamp)
	}); err != nil {
		return err
	}
	gate, err := s.deps.Gate(ctx, stamp)
	if err != nil {
		return err
	}
	due, err := DueJobs(ctx, s.db.Read, stamp)
	if err != nil {
		return err
	}
	admit := s.admit(gate)
	for _, job := range due {
		resources := claimResources(s.jobs, job)
		locks, err := db.TryResourceLocks(s.deps.DBPath, resources)
		if errors.As(err, new(*db.LockBusy)) {
			s.busy.Add(1)
			continue
		}
		if err != nil {
			return err
		}
		claim, skip, err := claimJob(ctx, s.db, job.ID, resources, stamp, s.owner(), admit)
		if claim == nil && skip == nil && err == nil {
			s.busy.Add(1)
		}
		if err != nil || claim == nil {
			_ = locks.Close()
		}
		if err != nil {
			return err
		}
		if skip != nil {
			s.started.Add(1)
			s.dirty.Store(true)
			if err := s.deps.Notice(ctx, *skip, stamp); err != nil {
				log.Error("skip notice failed", logx.Job, skip.Job.ID, logx.Err, err)
			}
		}
		if claim != nil {
			s.start(ctx, *claim, locks, s.work(claim.Job))
		}
	}
	return nil
}

func (s *Scheduler) recoverRuns(ctx context.Context) error {
	now := s.now()
	expired, err := expiredRuns(ctx, s.db.Read, now)
	if err != nil {
		return err
	}
	for _, p := range expired {
		resources := claimResources(s.jobs, p.job)
		locks, err := db.TryResourceLocks(s.deps.DBPath, resources)
		if errors.As(err, new(*db.LockBusy)) {
			s.busy.Add(1)
			continue
		}
		if err != nil {
			return err
		}
		claim, err := takeOver(ctx, s.db, p, resources, now, s.owner())
		if err != nil || claim == nil {
			_ = locks.Close()
		}
		if err != nil {
			return err
		}
		if claim != nil {
			s.start(ctx, *claim, locks, s.recovery)
		}
	}
	return nil
}

func (s *Scheduler) recovery(ctx context.Context, run Run) (Result, error) {
	threads, err := JobRunThreads(ctx, s.db.Read, run.Claim.JobRunID)
	if err != nil {
		return Result{}, err
	}
	if len(threads) == 0 {
		lost := workerLost
		return Result{Status: StatusError, Error: &lost}, nil
	}
	return s.deps.Recover(ctx, run, threads)
}

func (s *Scheduler) start(ctx context.Context, claim Claim, locks *db.LockSet, fn func(context.Context, Run) (Result, error)) {
	s.dirty.Store(true)
	s.started.Add(1)
	s.active.Add(1)
	s.wg.Go(func() {
		defer s.finished(ctx)
		defer func() { _ = locks.Close() }()
		s.execute(ctx, claim, fn)
	})
}

func (s *Scheduler) finished(ctx context.Context) {
	s.active.Add(-1)
	s.dirty.Store(true)
	s.checkpoint(ctx)
}

func (s *Scheduler) work(job Job) func(context.Context, Run) (Result, error) {
	def, declared := Declared(s.jobs, job)
	if !declared && !job.Fixed() {
		return s.deps.RunCron
	}
	if def.Run == nil {
		return func(context.Context, Run) (Result, error) {
			return Result{}, fmt.Errorf("no callable registered for %s", job.Name)
		}
	}
	for _, m := range def.Needs {
		if s.deps.Enabled != nil && !s.deps.Enabled(m) {
			return func(context.Context, Run) (Result, error) { return Result{Status: StatusSkippedModule}, nil }
		}
	}
	return def.Run
}

func (s *Scheduler) execute(ctx context.Context, claim Claim, fn func(context.Context, Run) (Result, error)) {
	timeout := 2 * time.Hour
	if def, ok := Declared(s.jobs, claim.Job); ok {
		timeout = def.Timeout
	}
	ctx, stopWork := context.WithTimeout(ctx, timeout)
	defer stopWork()
	work, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var lost atomic.Bool
	lose := func() {
		lost.Store(true)
		cancel(ErrClaimLost)
	}
	work = ledger.WithAdmission(work, func() error {
		if lost.Load() {
			return ErrClaimLost
		}
		return nil
	})
	stop := make(chan struct{})
	renewing := make(chan struct{})
	go func() {
		defer close(renewing)
		s.renew(context.WithoutCancel(ctx), claim, stop, lose)
	}()
	result, err := s.call(work, claim, fn)
	close(stop)
	<-renewing
	done, stopDone := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(TuningAgentSchedulerLeaseSeconds)*time.Second)
	defer stopDone()
	id := claim.Job.ID
	switch {
	case lost.Load():
		log.Warn(fmt.Sprintf("job %d lost its claim; it stopped and leaves the run to its successor", id), logx.Job, id)
		return
	case err != nil && interrupted(ctx, err):
		log.Info(fmt.Sprintf("job %d interrupted; its run resumes on restart", id), logx.Job, id)
		s.settle(done, claim, s.release(done, claim))
		return
	case err != nil:
		log.Error(fmt.Sprintf("job %d crashed", id), logx.Job, id, logx.Err, err)
		result = Result{Status: StatusError, ThreadID: result.ThreadID, Error: new(err.Error())}
	case result.Status == StatusRunning:
		s.settle(done, claim, s.release(done, claim))
		return
	}
	s.settle(done, claim, s.Finish(done, claim, result))
}

func (s *Scheduler) settle(ctx context.Context, claim Claim, err error) {
	id := claim.Job.ID
	switch {
	case errors.Is(err, ErrClaimLost):
		log.Warn(fmt.Sprintf("job %d lost its claim before settling; its successor owns the run", id), logx.Job, id)
	case err != nil:
		log.Error(fmt.Sprintf("job %d: recording the run failed", id), logx.Job, id, logx.Err, err)
	}
}

func (s *Scheduler) call(ctx context.Context, claim Claim, fn func(context.Context, Run) (Result, error)) (result Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			text := fmt.Sprintf("panic: %v", p)
			log.Error(fmt.Sprintf("job %d crashed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, text+"\n"+string(debug.Stack()))
			result, err = Result{}, errors.New(text)
		}
	}()
	return fn(ctx, Run{Claim: claim, Now: s.now})
}

func (s *Scheduler) renew(ctx context.Context, claim Claim, stop <-chan struct{}, lose func()) {
	interval := time.Duration(TuningAgentSchedulerRenewSeconds) * time.Second
	expires := claim.until
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	id := claim.Job.ID
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		attempt, cancel := context.WithTimeout(ctx, interval)
		if s.now() >= expires {
			cancel()
			lose()
			return
		}
		until, err := s.Renew(attempt, claim)
		cancel()
		switch {
		case err == nil:
			expires = until
		case errors.Is(err, ErrClaimLost):
			log.Warn(fmt.Sprintf("job %d: another invocation took over its claim", id), logx.Job, id)
			lose()
			return
		case s.now() >= expires:
			log.Error(fmt.Sprintf("job %d: its claim expired before it could be renewed", id), logx.Job, id, logx.Err, err)
			lose()
			return
		default:
			log.Warn(fmt.Sprintf("job %d: renewing its claim failed; retrying", id), logx.Job, id, logx.Err, err)
		}
	}
}

var ErrAllBusy = errors.New("scheduler: every due job is busy")

func (s *Scheduler) Once(ctx context.Context) error {
	var running int64
	if err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE r.status = ? AND j.lease_until >= ?", StatusRunning, s.now()).Scan(&running); err != nil {
		return err
	}
	before := s.started.Load()
	err := s.Cycle(ctx)
	s.wg.Wait()
	s.checkpoint(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	if s.started.Load() == before && (running > 0 || s.busy.Load() > 0) {
		return ErrAllBusy
	}
	return ctx.Err()
}

func (s *Scheduler) Cycle(ctx context.Context) error {
	if err := db.Ready(ctx, s.db); err != nil {
		return err
	}
	if err := s.recoverRuns(ctx); err != nil {
		return err
	}
	resumed, err := s.deps.Resume(ctx)
	if err != nil {
		return err
	}
	if resumed > 0 {
		s.dirty.Store(true)
	}
	if err := s.Tick(ctx); err != nil {
		return err
	}
	s.checkpoint(ctx)
	return nil
}

func (s *Scheduler) Run(ctx context.Context) {
	log.Info(fmt.Sprintf("scheduler %s started", s.deps.Owner))
	defer s.wg.Wait()
	for ctx.Err() == nil {
		s.beat()
		if err := s.safeCycle(ctx); err != nil && ctx.Err() == nil {
			log.Error("scheduler tick failed", logx.Err, err)
		}
		s.beat()
		select {
		case <-ctx.Done():
		case <-time.After(s.interval):
		}
	}
}

func (s *Scheduler) beat() {
	if s.deps.Beat != nil {
		s.deps.Beat()
	}
}

func (s *Scheduler) safeCycle(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("scheduler tick crashed: panic: %v\n%s", p, debug.Stack())
		}
	}()
	return s.Cycle(ctx)
}

func (s *Scheduler) idle(ctx context.Context) bool {
	return s.active.Load() == 0 && (s.deps.Idle == nil || s.deps.Idle(ctx))
}

func (s *Scheduler) checkpoint(ctx context.Context) {
	if ctx.Err() != nil || !s.dirty.Load() || !s.idle(ctx) {
		return
	}
	c, err := s.db.Checkpoint(ctx, time.Second)
	if err != nil {
		log.Error("wal checkpoint failed", logx.Err, err)
		return
	}
	if c.Busy {
		log.Debug("wal checkpoint busy; retrying at the next idle point")
		return
	}
	s.dirty.Store(false)
}
