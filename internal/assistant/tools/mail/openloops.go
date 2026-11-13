package mailtools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/openloops"
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
	enum(openloops.States[:]...) + `},"limit":` + loopsLimitBound.Schema() + `},` +
	`"additionalProperties":false}`)

var commitmentsSchema = json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":` +
	enum(openloops.CommitmentStatuses[:]...) + `},"limit":` + loopsLimitBound.Schema() + `},` +
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
		{
			Name: "commitments",
			Description: "List explicit commitments and deadlines extracted from mail (owner 'me' = the operator promised, " +
				"'them' = the other party). due is an explicit date or null. Status defaults to all.",
			InputSchema: commitmentsSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return commitments(ctx, tc, args, svc)
			},
			Effect:    tool.Read,
			Annotated: true,
			Marks:     tool.Messages | tool.Untrusted,
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
	state, err := oneOf(args, "state", openloops.States[:])
	if err != nil {
		return nil, err
	}
	limit, err := loopsLimitBound.Clamp(ctx, tc.DB.Read, args, "limit")
	if err != nil {
		return nil, err
	}
	loc, err := zone(ctx, tc.Env)
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

func commitments(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	status, err := oneOf(args, "status", openloops.CommitmentStatuses[:])
	if err != nil {
		return nil, err
	}
	limit, err := loopsLimitBound.Clamp(ctx, tc.DB.Read, args, "limit")
	if err != nil {
		return nil, err
	}
	read, err := svc(tc).Commitments(ctx, mailapp.CommitmentsInput{Status: status, Limit: int(limit)})
	if err != nil {
		return nil, err
	}
	items := read.Data.Commitments
	out := make([]any, len(items))
	marks := tool.Marks(0)
	for i, c := range items {
		subject := ""
		if c.Subject != nil {
			subject = *c.Subject
		}
		if c.Text != "" || subject != "" {
			marks |= tool.Messages | tool.Untrusted
		}
		out[i] = map[string]any{"text": c.Text, "owner": c.Owner, "due": nilString(c.Due), "status": c.Status, "message_id": c.MessageID, "subject": tool.Clip(subject, 200), "reminder_proposal_id": nilInt(c.ProposalID)}
	}
	return tool.Marked{Value: map[string]any{"commitments": out}, Marks: marks}, nil
}

func nilString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nilInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
