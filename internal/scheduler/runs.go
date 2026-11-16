package scheduler

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.agent.scheduler")

type Status string

const (
	StatusRunning             Status = "running"
	StatusOK                  Status = "ok"
	StatusError               Status = "error"
	StatusSkippedBudget       Status = "skipped_budget"
	StatusSkippedPause        Status = "skipped_pause"
	StatusSkippedModule       Status = "skipped_module"
	StatusSkippedUnconfigured Status = "skipped_unconfigured"
	StatusSkippedDependency   Status = "skipped_dependency"
	StatusMissed              Status = "missed_coalesced"
	StatusRequested           Status = "requested"
	StatusRefused             Status = "refused"
	workerLost                       = "worker lost"
)

func (s Status) Terminal() bool { return s != StatusRunning }

type Skipped struct {
	Job      Job
	JobRunID int64
	Status   Status
}

type Failure struct {
	Job      Job
	JobRunID int64
	ThreadID *int64
	Error    string
}

func (s *Scheduler) failed(ctx context.Context, claim Claim, result Result) {
	if s.deps.Failed == nil {
		return
	}
	var previous Status
	err := s.db.Read.QueryRowContext(ctx, "SELECT status FROM job_runs WHERE job_id = ? AND id < ? AND status IN (?, ?) ORDER BY id DESC LIMIT 1",
		claim.Job.ID, claim.JobRunID, StatusOK, StatusError).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Error("reading the previous outcome failed", logx.Job, claim.Job.ID, logx.Err, err)
		return
	}
	if previous == StatusError {
		return
	}
	f := Failure{Job: claim.Job, JobRunID: claim.JobRunID, ThreadID: result.ThreadID}
	if result.Error != nil {
		f.Error = *result.Error
	}
	if err := s.deps.Failed(ctx, f); err != nil {
		log.Error("failure notice failed", logx.Job, claim.Job.ID, logx.Err, err)
	}
}

type Admit func(Job) Status

func nextState(following *float64) State {
	if following == nil {
		return StateDisabled
	}
	return StateActive
}
