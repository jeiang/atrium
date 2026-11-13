package mailtools

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/jeiang/atrium/internal/config"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
)

func Definitions(svc func(*tool.Context) *mailapp.Service) []module.ToolDefinition {
	tools := Tools(svc)
	defs := make([]module.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = module.ToolDefinition{Tool: t}
	}
	return defs
}

func Tools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	var out []tool.Tool
	for _, group := range [][]tool.Tool{mailTools(svc), folderTools(svc), draftTools(svc), openLoopTools(svc), watchTools(svc), trackingTools(svc)} {
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
