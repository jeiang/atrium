package scheduler

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/runtime"
)

type ShutdownPolicy string

const (
	ShutdownDrain  ShutdownPolicy = "drain"
	ShutdownCancel ShutdownPolicy = "cancel"
)

type Spend uint8

const (
	AlwaysSpends Spend = iota
	ModelFree
	ConditionalSpends
)

type Status string

const (
	StatusOK            Status = "ok"
	StatusError         Status = "error"
	StatusSkippedBudget Status = "skipped_budget"
	StatusMissed        Status = "missed_coalesced"
)

type Run struct {
	JobID    int64
	JobRunID int64
	Missed   bool
	Now      func() float64
}

type Result struct {
	Status   Status
	ThreadID *int64
	Error    *string
	Tokens   int64
	Cost     float64
}

type Definition struct {
	Module          string
	Key             string
	Name            string
	Kind            string
	Prompt          string
	PromptRun       bool
	Schedule        string
	ScheduleSetting string
	InitialDelay    time.Duration
	Persistent      bool
	Overlap         bool
	Timeout         time.Duration
	Spend           Spend
	Backoff         runtime.Backoff
	Shutdown        ShutdownPolicy
	Resources       []string
	Needs           []string
	Run             func(context.Context, Run) (Result, error)
}
