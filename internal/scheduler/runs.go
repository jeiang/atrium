package scheduler

import "github.com/jeiang/atrium/internal/logx"

var log = logx.Logger("atrium.agent.scheduler")

type Status string

const (
	StatusRunning       Status = "running"
	StatusOK            Status = "ok"
	StatusError         Status = "error"
	StatusSkippedBudget Status = "skipped_budget"
	StatusSkippedPause  Status = "skipped_pause"
	StatusSkippedModule Status = "skipped_module"
	StatusMissed        Status = "missed_coalesced"
	workerLost                 = "worker lost"
)

func (s Status) Terminal() bool { return s != StatusRunning }

type Skipped struct {
	Job      Job
	JobRunID int64
	Status   Status
}

type Admit func(Job) Status

func nextState(following *float64) State {
	if following == nil {
		return StateDisabled
	}
	return StateActive
}
