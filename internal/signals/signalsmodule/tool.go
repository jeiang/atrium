package signalsmodule

import (
	"context"
	"encoding/json"
	"strings"

	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/signals"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
)

var loopKinds = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}

func enum(values ...string) string {
	b, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var loopsLimitBound = tool.NewCeiling(assistant.TuningAgentMailMaxLoopsLimit, 1, assistant.TuningAgentMailMaxLoopsLimit)

var openLoopsSchema = json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":` +
	enum(signals.States[:]...) + `},"limit":` + loopsLimitBound.Schema() + `},` +
	`"additionalProperties":false}`)

func ToolDefinitions() []module.ToolDefinition {
	return []module.ToolDefinition{
		{Tool: tool.Tool{
			Name: "open_loops",
			Description: "List mail threads that need follow-up: owe_reply (someone wrote last and awaits the operator) and " +
				"waiting_on_them (the operator wrote last and got no answer). Snoozed and dismissed loops are hidden.",
			InputSchema: openLoopsSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return openLoops(ctx, tc, args)
			},
			Effect:    tool.Read,
			Annotated: true,
			Marks:     tool.PII | tool.Messages | tool.Untrusted,
			Contexts:  loopKinds,
		}},
	}
}

func oneOf(args tool.Args, name string, allowed []string) (string, error) {
	value, _, err := tool.TextArg(args, name, false)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	for _, a := range allowed {
		if a == value {
			return value, nil
		}
	}
	return "", tool.Error(name + " must be one of " + strings.Join(allowed, ", "))
}

func openLoops(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	state, err := oneOf(args, "state", signals.States[:])
	if err != nil {
		return nil, err
	}
	limit, err := loopsLimitBound.Clamp(ctx, tc.DB.Read, args, "limit")
	if err != nil {
		return nil, err
	}
	loc, err := zone(ctx, tc)
	if err != nil {
		return nil, err
	}
	read, err := New(tc.DB, tc.Now, nil).OpenLoops(ctx, OpenLoopsInput{State: state, Limit: int(limit)})
	if err != nil {
		return nil, err
	}
	items := read.Data.Loops
	loops := make([]any, len(items))
	marks := tool.Marks(0)
	for i, l := range items {
		if l.Counterpart != "" {
			marks |= tool.PII
		}
		if l.Subject != "" {
			marks |= tool.Messages | tool.Untrusted
		}
		loops[i] = map[string]any{"state": l.State, "message_id": l.LastMessageID, "counterpart": l.Counterpart, "subject": tool.Clip(l.Subject, 200), "days_open": l.Days, "since": localTime(l.Since, loc).Format("2006-01-02T15:04"), "account": l.Account}
	}
	return tool.Marked{Value: map[string]any{"loops": loops, "note": "owe_reply: the last message in the thread is theirs and awaits the operator; waiting_on_them: the operator wrote last."}, Marks: marks}, nil
}

func zone(ctx context.Context, tc *tool.Context) (*time.Location, error) {
	loc, err := scheduler.Zone(config.From(ctx).TZName(tc.Env))
	if err != nil {
		return nil, tool.Error("invalid time zone")
	}
	return loc, nil
}

func localTime(ts float64, loc *time.Location) time.Time {
	sec, us := timex.SplitTimestamp(ts)
	return time.Unix(sec, us*1000).In(loc)
}
