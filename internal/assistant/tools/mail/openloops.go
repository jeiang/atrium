package mailtools

import (
	"context"
	"encoding/json"
	"strings"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
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

var loopsLimitBound = tool.NewCeiling(mailapp.AgentMailMaxLoopsLimit, 1, mailapp.AgentMailMaxLoopsLimit)

var openLoopsSchema = json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":` +
	enum(mailapp.LoopStates[:]...) + `},"limit":` + loopsLimitBound.Schema() + `},` +
	`"additionalProperties":false}`)

func openLoopTools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "open_loops",
			Description: "List mail threads that need follow-up: owe_reply (someone wrote last and awaits the operator) and " +
				"waiting_on_them (the operator wrote last and got no answer). Snoozed and dismissed loops are hidden.",
			InputSchema: openLoopsSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return openLoops(ctx, tc, args, svc)
			},
			Effect:    tool.Read,
			Annotated: true,
			Marks:     tool.PII | tool.Messages | tool.Untrusted,
			Contexts:  loopKinds,
		},
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

func openLoops(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	state, err := oneOf(args, "state", mailapp.LoopStates[:])
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
	read, err := svc(tc).OpenLoops(ctx, mailapp.OpenLoopsInput{State: state, Limit: int(limit)})
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
