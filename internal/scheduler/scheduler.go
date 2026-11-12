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
	"github.com/jeiang/atrium/internal/logx"
)

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

	wg     sync.WaitGroup
	active atomic.Int64
	dirty  atomic.Bool
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
		locks, err := db.TryResourceLocks(s.deps.DBPath, claimResources(s.jobs, job))
		if errors.As(err, new(*db.LockBusy)) {
			continue
		}
		if err != nil {
			return err
		}
		claim, skip, err := claimJob(ctx, s.db, job.ID, stamp, s.deps.Owner, admit)
		if err != nil || claim == nil {
			_ = locks.Close()
		}
		if err != nil {
			return err
		}
		if skip != nil {
			s.dirty.Store(true)
			if err := s.deps.Notice(ctx, *skip, stamp); err != nil {
				log.Error("skip notice failed", logx.Job, skip.Job.ID, logx.Err, err)
			}
		}
		if claim == nil {
			continue
		}
		s.dirty.Store(true)
		s.active.Add(1)
		s.wg.Go(func() {
			defer s.finished(ctx)
			defer func() { _ = locks.Close() }()
			s.execute(ctx, *claim)
		})
	}
	return nil
}

func (s *Scheduler) finished(ctx context.Context) {
	s.active.Add(-1)
	s.dirty.Store(true)
	s.checkpoint(ctx)
}

func (s *Scheduler) execute(ctx context.Context, claim Claim) {
	defer func() {
		if p := recover(); p != nil {
			text := fmt.Sprintf("panic: %v", p)
			log.Error(fmt.Sprintf("job %d crashed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, text+"\n"+string(debug.Stack()))
			s.finish(context.WithoutCancel(ctx), claim, Result{Status: StatusError, Error: &text})
		}
	}()
	job := claim.Job
	run := Run{Claim: claim, Now: s.now}
	def, declared := Declared(s.jobs, job.Name)
	if !declared {
		s.record(ctx, claim, s.deps.RunCron, run)
		return
	}
	if def.Run == nil {
		s.failed(ctx, claim, nil, fmt.Errorf("no callable registered for %s", job.Name))
		return
	}
	for _, m := range def.Needs {
		if s.deps.Enabled != nil && !s.deps.Enabled(m) {
			s.finish(ctx, claim, Result{Status: StatusSkippedModule})
			return
		}
	}
	s.record(ctx, claim, def.Run, run)
}

func (s *Scheduler) record(ctx context.Context, claim Claim, fn func(context.Context, Run) (Result, error), run Run) {
	result, err := fn(ctx, run)
	if err != nil {
		s.failed(ctx, claim, result.ThreadID, err)
		return
	}
	s.finish(ctx, claim, result)
}

func (s *Scheduler) finish(ctx context.Context, claim Claim, r Result) {
	if err := FinishRun(ctx, s.db, claim, s.now(), r); err != nil {
		log.Error(fmt.Sprintf("job %d: recording the run failed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, err)
	}
}

func (s *Scheduler) failed(ctx context.Context, claim Claim, threadID *int64, err error) {
	if interrupted(ctx, err) {
		log.Info(fmt.Sprintf("job %d interrupted; its run resumes on restart", claim.Job.ID), logx.Job, claim.Job.ID)
		return
	}
	log.Error(fmt.Sprintf("job %d crashed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, err)
	s.finish(ctx, claim, Result{Status: StatusError, ThreadID: threadID, Error: new(err.Error())})
}

func (s *Scheduler) Cycle(ctx context.Context) error {
	if err := db.Ready(ctx, s.db); err != nil {
		return err
	}
	if err := Recover(ctx, s.db, s.deps.DBPath, s.now(), s.deps.Recover); err != nil {
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
