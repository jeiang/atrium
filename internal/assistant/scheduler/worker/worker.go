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
)

var log = logx.Logger("atrium.agent.scheduler")

type Env struct {
	Service *service.Service
	Now     func() float64
}

type Result struct{ RunID int64 }

type Callable struct {
	Modules []string
	Run     func(ctx context.Context, e Env) (Result, error)
}

type Worker struct {
	Service   *service.Service
	Callables map[string]Callable
	Interval  time.Duration
	Owner     string
	Beat      func()
	Enabled   func(string) bool

	wg     sync.WaitGroup
	active atomic.Int64
	dirty  atomic.Bool
}

func New(ctx context.Context, svc *service.Service, callables map[string]Callable, enabled func(string) bool) (*Worker, error) {
	r := assistant.Read(ctx, svc.DB.Read)
	tick := r.Int(scheduler.KeyTickSeconds)
	if err := r.Err(); err != nil {
		return nil, err
	}
	return &Worker{Service: svc, Callables: callables, Interval: time.Duration(tick) * time.Second, Owner: journal.NewOwner(), Enabled: enabled}, nil
}

func (w *Worker) now() float64 {
	if w.Service.Now != nil {
		return w.Service.Now()
	}
	return float64(time.Now().UnixMicro()) / 1e6
}

func (w *Worker) spawn(fn func()) { w.wg.Go(fn) }

func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func (w *Worker) Tick(ctx context.Context) error {
	d, env := w.Service.DB, w.Service.Env
	stamp := w.now()
	tz := config.From(ctx).TZName(env)
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return err
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		return scheduler.Ensure(ctx, tx, tz, stamp)
	})
	if err != nil {
		return err
	}
	claims, skipped, err := scheduler.ClaimDue(ctx, d, w.Service.Cfg, loc, stamp, w.Owner)
	if err != nil {
		return err
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
		w.spawn(func() {
			defer w.finished(ctx)
			w.ExecuteClaim(ctx, claim)
		})
	}
	return nil
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
	var previous scheduler.Status
	err := w.Service.DB.Read.QueryRowContext(ctx, "SELECT status FROM job_runs WHERE job_id = ? AND id < ? ORDER BY id DESC LIMIT 1",
		skip.Job.ID, skip.JobRunID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous == scheduler.StatusSkippedBudget {
		return nil
	}
	if !notify.Enabled(ctx) {
		return err
	}
	loc, err := config.From(ctx).Location(w.Service.Env)
	if err != nil {
		return err
	}
	st, err := budget.StatusAt(ctx, w.Service.DB.Read, now, loc)
	if err != nil {
		return err
	}
	r := assistant.Read(ctx, w.Service.DB.Read)
	pause := r.Float(assistant.KeyBudgetUnattended)
	if err := r.Err(); err != nil {
		return err
	}
	body := fmt.Sprintf("Monthly budget is at $%.2f of $%.2f (%.0f%%); unattended runs pause at %.0f%%. This scheduled run did not start.",
		st.SpentUSD, st.BudgetUSD, st.Share*100, pause*100)
	_, err = notify.Send(ctx, w.Service.DB.Write, notify.Message{
		Channel: notify.System,
		Title:   "Skipped: " + skip.Job.Name,
		Body:    body,
		Now:     func() float64 { return now },
	})
	return err
}

func (w *Worker) ExecuteClaim(ctx context.Context, claim scheduler.Claim) (status scheduler.Status) {
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
		if call.Run == nil {
			return w.failedRun(ctx, claim, nil, fmt.Errorf("no callable registered for %s", job.Name))
		}
		return w.executeCallable(ctx, claim, call)
	}
	return w.executePrompt(ctx, claim)
}

func (w *Worker) callable(job scheduler.Job) (Callable, bool) {
	spec, ok := scheduler.BuiltinFor(job.Name)
	if !ok || spec.Run != scheduler.CallableRun {
		return Callable{}, false
	}
	return w.Callables[job.Name], true
}

func (w *Worker) finish(ctx context.Context, claim scheduler.Claim, o scheduler.Outcome) error {
	err := scheduler.FinishRun(ctx, w.Service.DB, claim, w.now(), o)
	if err != nil {
		log.Error(fmt.Sprintf("job %d: recording the run failed", claim.Job.ID), logx.Job, claim.Job.ID, logx.Err, err)
	}
	return err
}

func failed(err error) *string { return new(err.Error()) }

func (w *Worker) settle(ctx context.Context, claim scheduler.Claim, runID int64, threadID *int64) (scheduler.Status, error) {
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
	case run.State != journal.Done && run.Error != nil && *run.Error == budget.SpendCapText:
		o.Status = scheduler.StatusSkippedBudget
		o.Error = run.Error
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

func (w *Worker) executePrompt(ctx context.Context, claim scheduler.Claim) scheduler.Status {
	job, d := claim.Job, w.Service.DB
	kind, prompt := threads.Cron, job.Prompt
	if job.Kind == scheduler.JobNightly {
		kind, prompt = threads.Nightly, nightly.Prompt
	}
	if spec, ok := scheduler.BuiltinFor(job.Name); ok && spec.Run == scheduler.PromptRun {
		prompt = spec.Prompt
	}
	operator := job.Kind == scheduler.JobCron && job.PromptBy != nil && *job.PromptBy == scheduler.Operator
	var threadID *int64
	runID, err := func() (int64, error) {
		spec := threads.Spec{Kind: kind, Title: &job.Name, JobID: &job.ID}
		if !operator {
			marks, err := threads.MarksOf(ctx, d.Read, []string{fmt.Sprintf("schedule:%d", job.ID)})
			if err != nil {
				return 0, err
			}
			if marks != 0 {
				now := w.now()
				spec.Marks = threads.Marks{At: &now, Set: marks}
			}
		}
		id, err := threads.Create(ctx, d.Write, w.now(), spec)
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

func (w *Worker) failedRun(ctx context.Context, claim scheduler.Claim, threadID *int64, err error) scheduler.Status {
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

func (w *Worker) executeCallable(ctx context.Context, claim scheduler.Claim, call Callable) scheduler.Status {
	for _, m := range call.Modules {
		if w.Enabled != nil && !w.Enabled(m) {
			_ = w.finish(ctx, claim, scheduler.Outcome{Status: scheduler.StatusSkippedModule})
			return scheduler.StatusSkippedModule
		}
	}
	result, err := call.Run(ctx, Env{Service: w.Service, Now: w.now})
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
	if resumed > 0 {
		w.dirty.Store(true)
	}
	if err := w.Tick(ctx); err != nil {
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
