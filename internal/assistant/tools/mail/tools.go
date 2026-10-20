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

func Tools() []tool.Tool {
	var out []tool.Tool
	for _, group := range [][]tool.Tool{mailTools(), draftTools(), openLoopTools(), watchTools(), contactTools()} {
		out = append(out, group...)
	}
	return out
}

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

func raised(err error) error {
	var c py.Classed
	if errors.As(err, &c) {
		return &tool.Raised{Class: c.PyClass()}
	}
	return err
}
