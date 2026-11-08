package mailtools

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/timex"
)

func Tools() []tool.Tool {
	RegisterProposalKinds()
	var out []tool.Tool
	for _, group := range [][]tool.Tool{mailTools(), folderTools(), draftTools(), openLoopTools(), watchTools(), trackingTools()} {
		out = append(out, group...)
	}
	return out
}

func intArg(value any, message string) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, tool.Error(message)
		}
		return i, nil
	case *big.Int:
		return 0, tool.Error("value is too large")
	}
	return 0, tool.Error(message)
}

func zone(ctx context.Context, env config.Env) (*time.Location, error) {
	loc, err := scheduler.Zone(config.From(ctx).TZName(env))
	if err != nil {
		return nil, tool.Error("invalid time zone")
	}
	return loc, nil
}

func localTime(ts float64, loc *time.Location) time.Time {
	sec, us := timex.SplitTimestamp(ts)
	return time.Unix(sec, us*1000).In(loc)
}
