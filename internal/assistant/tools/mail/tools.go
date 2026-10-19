// Package mailtools is the agent tools of the mail side: agent/tools/{mail,drafts,openloops,watches,contacts}.py.
// Mail text reaches the model only as untrusted data, and no tool here sends mail or writes a mailbox: the two
// draft tools write the drafts table, and only the operator's Drafts page sends.
package mailtools

import (
	"errors"
	"math/big"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
)

// Tools is TOOLS of every module this package ports.
func Tools() []tool.Tool {
	var out []tool.Tool
	for _, group := range [][]tool.Tool{mailTools(), draftTools(), openLoopTools(), watchTools(), contactTools()} {
		out = append(out, group...)
	}
	return out
}

// intArg is the `isinstance(value, int)` check the tools make by hand: a bool, a float and any other type fail.
// An integer beyond SQLite's range is the OverflowError Python's sqlite3 raises when it is bound.
func intArg(value any, message string) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case *big.Int:
		return 0, &tool.Raised{Class: "OverflowError"}
	}
	return 0, tool.Error(message)
}

func zone(env config.Env) (*time.Location, error) {
	loc, err := scheduler.Zone(config.TZName(env))
	if err != nil {
		return nil, &tool.Raised{Class: "ScheduleError"}
	}
	return loc, nil
}

func localTime(ts float64, loc *time.Location) time.Time {
	sec, us := py.SplitTimestamp(ts)
	return time.Unix(sec, us*1000).In(loc)
}

// raised is the loop's view of a Python exception: tool.Raised with the class, or err itself when it has none.
func raised(err error) error {
	var c py.Classed
	if errors.As(err, &c) {
		return &tool.Raised{Class: c.PyClass()}
	}
	return err
}
