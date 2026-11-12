package scheduler

import (
	"time"

	"github.com/jeiang/atrium/internal/runtime"
)

type ShutdownPolicy string

const (
	ShutdownDrain  ShutdownPolicy = "drain"
	ShutdownCancel ShutdownPolicy = "cancel"
)

type Definition struct {
	Key             string
	Schedule        string
	ScheduleSetting string
	InitialDelay    time.Duration
	Persistent      bool
	Overlap         bool
	Timeout         time.Duration
	Spends          bool
	Backoff         runtime.Backoff
	Shutdown        ShutdownPolicy
	Resources       []string
	Needs           []string
}
