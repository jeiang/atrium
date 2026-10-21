package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/nightly"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.agent.scheduler")

type Env struct {
	DB      *db.DB
	Cfg     config.Config
	Env     config.Env
	Service *service.Service
	Now     func() float64
}

type Result struct{ RunID int64 }

type Callable func(ctx context.Context, e Env) (Result, error)

type Worker struct {
	Service   *service.Service
	Callables map[string]Callable
	Interval  time.Duration
	Owner     string
	Spawn     func(name string, fn func())
	Beat      func()

	wg     sync.WaitGroup
	active atomic.Int64
	dirty  atomic.Bool
}

func New(ctx context.Context, svc *service.Service, callables map[string]Callable) (*Worker, error) {
	r := assistant.Read(ctx, svc.DB.Read)
	tick := r.Int(scheduler.KeyTickSeconds)
	if err := r.Err(); err != nil {
		return nil, err
	}
	return &Worker{Service: svc, Callables: callables, Interval: time.Duration(tick) * time.Second, Owner: journal.NewOwner()}, nil
}

func (w *Worker) now() float64 {
	if w.Service.Now != nil {
		return w.Service.Now()
	}
	return float64(time.Now().UnixMicro()) / 1e6
}

func (w *Worker) spawn(name string, fn func()) {
	if w.Spawn != nil {
		w.Spawn(name, fn)
		return
	}
	w.wg.Go(fn)
}

func (w *Worker) Wait() { w.wg.Wait() }

func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func (w *Worker) Tick(ctx context.Context) ([]scheduler.Claim, []scheduler.Skipped, error) {
	d, env := w.Service.DB, w.Service.Env
	stamp := w.now()
	tz := config.TZName(env)
	loc, err := config.Location(env)
	if err != nil {
		return nil, nil, err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := scheduler.EnsureNightly(ctx, tx, tz, stamp); err != nil {
			return err
		}
		return scheduler.EnsureBuiltins(ctx, tx, tz, stamp)
	})
	if err != nil {
		return nil, nil, err
	}
	claims, skipped, err := scheduler.ClaimDue(ctx, d, w.Service.Cfg, loc, stamp, w.Owner)
	if err != nil {
		return nil, nil, err
	}
	if len(claims)+len(skipped) > 0 {
		w.dirty.Store(true)
	}
	for _, skip := range skipped {
		if err := w.notifySkipped(ctx, skip, stamp); err != nil {
			log.Error("skip notice failed", logx.Job, skip.Job.ID, logx.Err, err)
		}
	}
	for _, claim := range claims {
		w.active.Add(1)
		w.spawn(fmt.Sprintf("atrium-job-%d", claim.Job.ID), func() {
			defer w.finished(ctx)
			w.ExecuteClaim(ctx, claim)
		})
	}
	return claims, skipped, nil
}

func (w *Worker) finished(ctx context.Context) {
	w.active.Add(-1)
	w.dirty.Store(true)
	w.checkpoint(ctx)
}

func (w *Worker) notifySkipped(ctx context.Context, skip scheduler.Skipped, now float64) error {
	if skip.Status != scheduler.StatusSkippedBudget {
		return nil
	}
	var previous string
	err := w.Service.DB.Read.QueryRowContext(ctx, "SELECT status FROM job_runs WHERE job_id = ? AND id < ? ORDER BY id DESC LIMIT 1",
		skip.Job.ID, skip.JobRunID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous == scheduler.StatusSkippedBudget {
		return nil
	}
	_, err = notify.Send(ctx, w.Service.DB.Write, w.Service.Env, notify.Message{
		Title: "Skipped: " + skip.Job.Name,
		Body:  "Monthly budget is above 80%, so this scheduled run did not start.",
		Now:   func() float64 { return now },
	})
	return err
}

func (w *Worker) ExecuteClaim(ctx context.Context, claim scheduler.Claim) (status string) {
	defer func() {
		if p := recover(); p != nil {
			text := fmt.Sprintf("panic: %v", p)
			log.Error(fmt.Sprintf("job %d crashed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, text+"\n"+string(debug.Stack()))
			status = scheduler.StatusError
			_ = w.finish(context.WithoutCancel(ctx), claim, scheduler.Outcome{Status: status, Error: &text})
		}
	}()
	job := claim.Job
	if call, isCallable := w.callable(job); isCallable {
		return w.executeCallable(ctx, claim, call)
	}
	return w.executePrompt(ctx, claim)
}

func (w *Worker) callable(job scheduler.Job) (Callable, bool) {
	if job.Kind != scheduler.JobBuiltin {
		return nil, false
	}
	for _, name := range scheduler.CallableNames {
		if name == job.Name {
			return w.Callables[name], true
		}
	}
	return nil, false
}

func (w *Worker) finish(ctx context.Context, claim scheduler.Claim, o scheduler.Outcome) error {
	err := scheduler.FinishRun(ctx, w.Service.DB.Write, claim, w.now(), o)
	if err != nil {
		log.Error(fmt.Sprintf("job %d: recording the run failed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, err)
	}
	return err
}

func failed(err error) *string { return new(py.ExceptionText(err)) }

func (w *Worker) settle(ctx context.Context, claim scheduler.Claim, runID int64, threadID *int64) (string, error) {
	d := w.Service.DB
	run, err := journal.GetRun(ctx, d.Read, runID)
	if err != nil {
		return "", err
	}
	tokens, err := scheduler.RunTokens(ctx, d.Read, runID)
	if err != nil {
		return "", err
	}
	cost, err := usage.RunCost(ctx, d.Read, runID)
	if err != nil {
		return "", err
	}
	o := scheduler.Outcome{Status: scheduler.StatusOK, ThreadID: threadID, Tokens: tokens, Cost: cost}
	switch {
	case run.State != journal.Done:
		o.Status = scheduler.StatusError
		text := run.State
		if run.Error != nil && *run.Error != "" {
			text = *run.Error
		}
		o.Error = &text
	case claim.Missed:
		o.Status = scheduler.StatusMissed
	}
	return o.Status, w.finish(ctx, claim, o)
}

func (w *Worker) executePrompt(ctx context.Context, claim scheduler.Claim) string {
	job, d := claim.Job, w.Service.DB
	kind, prompt := threads.Cron, job.Prompt
	if job.Kind == scheduler.JobNightly {
		kind, prompt = threads.Nightly, nightly.Prompt
	}
	if job.Kind == scheduler.JobBuiltin {
		if code, ok := scheduler.BuiltinPrompt(job.Name); ok {
			prompt = code
		}
	}
	operator := job.Kind == scheduler.JobCron && job.PromptBy != nil && *job.PromptBy == scheduler.Operator
	var threadID *int64
	runID, err := func() (int64, error) {
		id, err := service.CreateThread(ctx, d.Write, w.now(), threads.NewThread{Kind: kind, Title: &job.Name, JobID: &job.ID})
		if err != nil {
			return 0, err
		}
		threadID = &id
		if _, err := d.Write.ExecContext(ctx, "UPDATE job_runs SET thread_id = ? WHERE id = ?", id, claim.JobRunID); err != nil {
			return 0, err
		}
		return w.Service.RunOnce(ctx, id, prompt, service.Start{Kind: kind, Operator: operator})
	}()
	if err != nil {
		return w.failedRun(ctx, claim, threadID, err)
	}
	status, err := w.settle(ctx, claim, runID, threadID)
	if err != nil {
		return w.failedRun(ctx, claim, threadID, err)
	}
	return status
}

func (w *Worker) failedRun(ctx context.Context, claim scheduler.Claim, threadID *int64, err error) string {
	if interrupted(ctx, err) {
		log.Info(fmt.Sprintf("job %d interrupted; its run resumes on restart", claim.Job.ID), logx.Job, claim.Job.ID)
		return ""
	}
	if budgetErr := new(budget.BudgetExceeded); errors.As(err, budgetErr) {
		_ = w.finish(ctx, claim, scheduler.Outcome{Status: scheduler.StatusSkippedBudget, ThreadID: threadID, Error: new(budgetErr.Error())})
		return scheduler.StatusSkippedBudget
	}
	log.Error(fmt.Sprintf("job %d crashed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, err)
	_ = w.finish(ctx, claim, scheduler.Outcome{Status: scheduler.StatusError, ThreadID: threadID, Error: failed(err)})
	return scheduler.StatusError
}

func (w *Worker) executeCallable(ctx context.Context, claim scheduler.Claim, call Callable) string {
	result, err := call(ctx, Env{DB: w.Service.DB, Cfg: w.Service.Cfg, Env: w.Service.Env, Service: w.Service, Now: w.now})
	if err != nil {
		return w.failedRun(ctx, claim, nil, err)
	}
	if result.RunID == 0 {
		status := scheduler.StatusOK
		if claim.Missed {
			status = scheduler.StatusMissed
		}
		_ = w.finish(ctx, claim, scheduler.Outcome{Status: status})
		return status
	}
	run, err := journal.GetRun(ctx, w.Service.DB.Read, result.RunID)
	if err != nil {
		return w.failedRun(ctx, claim, nil, err)
	}
	var threadID *int64
	if run != nil {
		threadID = &run.ThreadID
		status, err := w.settle(ctx, claim, result.RunID, threadID)
		if err != nil {
			return w.failedRun(ctx, claim, threadID, err)
		}
		return status
	}
	status := scheduler.StatusOK
	if claim.Missed {
		status = scheduler.StatusMissed
	}
	_ = w.finish(ctx, claim, scheduler.Outcome{Status: status})
	return status
}

func (w *Worker) Cycle(ctx context.Context) error {
	d := w.Service.DB
	if err := db.Ready(ctx, d); err != nil {
		return err
	}
	if err := scheduler.SettleResumed(ctx, d, w.now()); err != nil {
		return err
	}
	if err := scheduler.ReleaseStale(ctx, d, w.now()); err != nil {
		return err
	}
	resumed, err := w.Service.ResumeInterrupted(ctx, true)
	if err != nil {
		return err
	}
	if len(resumed) > 0 {
		w.dirty.Store(true)
	}
	if _, _, err := w.Tick(ctx); err != nil {
		return err
	}
	w.checkpoint(ctx)
	return nil
}

func (w *Worker) Run(ctx context.Context) {
	log.Info(fmt.Sprintf("scheduler %s started", w.Owner))
	defer w.wg.Wait()
	for ctx.Err() == nil {
		w.beat()
		if err := w.safeCycle(ctx); err != nil && ctx.Err() == nil {
			log.Error("scheduler tick failed", logx.Err, err)
		}
		w.beat()
		select {
		case <-ctx.Done():
		case <-time.After(w.Interval):
		}
	}
}

func (w *Worker) beat() {
	if w.Beat != nil {
		w.Beat()
	}
}

func (w *Worker) safeCycle(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("scheduler tick crashed: panic: %v\n%s", p, debug.Stack())
		}
	}()
	return w.Cycle(ctx)
}
