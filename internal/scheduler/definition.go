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

type Run struct {
	Claim Claim
	Now   func() float64
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
	Every           time.Duration
	ScheduleSetting string
	ScheduleHost    string
	InitialDelay    time.Duration
	Persistent      bool
	RunsWhilePaused bool
	Overlap         bool
	Timeout         time.Duration
	Spend           Spend
	Backoff         runtime.Backoff
	Shutdown        ShutdownPolicy
	Resources       []string
	Needs           []string
	Run             func(context.Context, Run) (Result, error)
	Recover         Recovery
}
