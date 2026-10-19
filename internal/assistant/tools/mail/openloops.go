package mailtools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/py"
)

const maxLimit = 100

var loopKinds = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}

func enum(values ...string) string {
	b, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var openLoopsSchema = json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":` +
	enum(openloops.States[:]...) + `},"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(maxLimit) + `}},` +
	`"additionalProperties":false}`)

var commitmentsSchema = json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":` +
	enum(openloops.CommitmentStatuses[:]...) + `},"limit":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(maxLimit) + `}},` +
	`"additionalProperties":false}`)

func openLoopTools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "open_loops",
			Description: "List mail threads that need follow-up: owe_reply (someone wrote last and awaits the operator) and " +
				"waiting_on_them (the operator wrote last and got no answer). Snoozed and dismissed loops are hidden.",
			InputSchema: openLoopsSchema,
			Handler:     openLoops,
			Effect:      tool.Read,
			Contexts:    loopKinds,
		},
		{
			Name: "commitments",
			Description: "List explicit commitments and deadlines extracted from mail (owner 'me' = the operator promised, " +
				"'them' = the other party). due is an explicit date or null. Status defaults to all.",
			InputSchema: commitmentsSchema,
			Handler:     commitments,
			Effect:      tool.Read,
			Contexts:    loopKinds,
		},
	}
}

func oneOf(args py.Dict, name string, allowed []string) (string, error) {
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

func openLoops(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	state, err := oneOf(args, "state", openloops.States[:])
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 20, maxLimit, 1)
	if err != nil {
		return nil, err
	}
	now := tc.Now()
	loc, err := zone(tc.Env)
	if err != nil {
		return nil, err
	}
	items, err := openloops.ListLoops(ctx, tc.DB.Read, now, state, int(limit), false)
	if err != nil {
		return nil, err
	}
	loops := make([]any, len(items))
	for i, l := range items {
		loops[i] = py.Dict{
			{Key: "state", Value: l.State},
			{Key: "message_id", Value: l.LastMessageID},
			{Key: "counterpart", Value: l.Counterpart},
			{Key: "subject", Value: tool.Clip(l.Subject, 200)},
			{Key: "days_open", Value: l.Days},
			{Key: "since", Value: localTime(l.Since, loc).Format("2006-01-02T15:04")},
			{Key: "account", Value: l.Account},
		}
	}
	return py.Dict{
		{Key: "loops", Value: loops},
		{Key: "note", Value: "owe_reply: the last message in the thread is theirs and awaits the operator; waiting_on_them: the operator wrote last."},
	}, nil
}

func commitments(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	status, err := oneOf(args, "status", openloops.CommitmentStatuses[:])
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 20, maxLimit, 1)
	if err != nil {
		return nil, err
	}
	items, err := openloops.ListCommitments(ctx, tc.DB.Read, status, int(limit))
	if err != nil {
		return nil, err
	}
	out := make([]any, len(items))
	for i, c := range items {
		subject := ""
		if c.Subject != nil {
			subject = *c.Subject
		}
		out[i] = py.Dict{
			{Key: "text", Value: c.Text},
			{Key: "owner", Value: c.Owner},
			{Key: "due", Value: nilString(c.Due)},
			{Key: "status", Value: c.Status},
			{Key: "message_id", Value: c.MessageID},
			{Key: "subject", Value: tool.Clip(subject, 200)},
			{Key: "reminder_proposal_id", Value: nilInt(c.ProposalID)},
		}
	}
	return py.Dict{{Key: "commitments", Value: out}}, nil
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
