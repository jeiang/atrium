package registry

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/permission"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

type Situation uint8

const (
	Clean Situation = iota
	Kinds
	PII
	UntrustedKnowledge
	UntrustedKinds
	UntrustedIsolated
)

const (
	channelCount   = 4
	situationCount = 6
)

func PromptCarriesKnowledge(kind tool.Kind) bool { return kind != tool.Sub }

func Classify(m tool.Marks, knowledge, privateTools bool) Situation {
	untrusted := m&tool.Untrusted != 0
	switch {
	case untrusted && m.Kinds() != 0:
		return UntrustedKinds
	case untrusted && (knowledge || privateTools):
		return UntrustedKnowledge
	case untrusted:
		return UntrustedIsolated
	case m&tool.PII != 0:
		return PII
	case m.Kinds() != 0:
		return Kinds
	default:
		return Clean
	}
}

type Table [channelCount][situationCount]Decision

func DefaultTable() Table {
	return Table{
		{Allowed, Allowed, Allowed, Allowed, Allowed, Allowed},
		{Allowed, Allowed, Allowed, Allowed, Allowed, Allowed},
		{Ask, Ask, Ask, Ask, Ask, Ask},
		{Allowed, Ask, Denied, Ask, Denied, Allowed},
	}
}

var floors = Table{
	{Allowed, Allowed, Allowed, Allowed, Allowed, Allowed},
	{Allowed, Allowed, Allowed, Allowed, Allowed, Allowed},
	{Allowed, Allowed, Allowed, Ask, Ask, Allowed},
	{Allowed, Allowed, Allowed, Ask, Ask, Allowed},
}

var (
	PermissionChannelLabels   = [...]string{"Own accounts", "Notify me", "Other people", "Internet"}
	PermissionSituationLabels = [...]string{"Nothing marked", "Messages or personal", "Contact details", "Outside text", "Outside text with private data", "Isolated helper"}
)

var (
	channelKeys   = [...]string{"own", "notify", "people", "internet"}
	situationKeys = [...]string{"clean", "kinds", "pii", "untrusted_knowledge", "untrusted_kinds", "untrusted_isolated"}
	decisionNames = map[Decision]string{Allowed: "allow", Ask: "ask", Denied: "deny"}
)

func PermissionKey(channel, situation int) string {
	return "permission_" + channelKeys[channel] + "_" + situationKeys[situation]
}

func init() {
	table := DefaultTable()
	for i := range channelKeys {
		for s := range situationKeys {
			choices := []string{"allow", "ask", "deny"}
			if (i == 2 || i == 3) && (s == 3 || s == 4) {
				choices = []string{"ask", "deny"}
			}
			settings.MustRegister(settings.Value(settings.UI, settings.Def{
				Key: PermissionKey(i, s), Module: "permissions", Type: settings.Enum,
				Choices: choices, Default: decisionNames[table[i][s]],
			}))
		}
	}
}

func parseDecision(value string) (Decision, bool) {
	switch value {
	case "allow":
		return Allowed, true
	case "ask":
		return Ask, true
	case "deny":
		return Denied, true
	}
	return Allowed, false
}

func (r *Registry) loadTable(ctx context.Context, q db.Querier) (Table, error) {
	table := DefaultTable()
	if r.settings == nil || q == nil {
		return table, nil
	}
	for i := range channelKeys {
		for s := range situationKeys {
			found, err := r.settings.Resolve(ctx, q, PermissionKey(i, s))
			if err != nil {
				return Table{}, err
			}
			decision, ok := parseDecision(found.Value)
			if !ok {
				continue
			}
			table[i][s] = max(decision, floors[i][s])
		}
	}
	return table, nil
}

func (t Table) Decide(c tool.Channel, s Situation, kind tool.Kind) Decision {
	if c == tool.ChannelNone {
		return Allowed
	}
	if !c.Valid() || s >= situationCount {
		return Denied
	}
	i := int(c) - 1
	return max(t[i][s], floors[i][s])
}

func hasCard(t tool.Tool) bool { return t.Approval != nil || t.Effect == tool.Propose }

func Gate(t tool.Tool, table Table, s Situation, kind tool.Kind) tool.Tool {
	real := t.Handler
	t.Handler = func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		channel := t.Channel
		if t.ChannelOf != nil {
			channel = t.ChannelOf(args)
		}
		decision := table.Decide(channel, s, kind)
		if decision == Ask && !hasCard(t) && kind != tool.Chat {
			decision = Denied
		}
		switch decision {
		case Allowed:
			return real(ctx, tc, args)
		case Denied:
			return nil, tool.Error(t.Name + " is not allowed in this thread by the permission table")
		}
		return ask(ctx, tc, t, args, real)
	}
	return t
}

func ask(ctx context.Context, tc *tool.Context, t tool.Tool, args tool.Args, real tool.Handler) (any, error) {
	payload, err := permission.NewPayload(t.Name, tc, args)
	if err != nil {
		return nil, err
	}
	title := permission.Title(t.Name)
	id, _, err := proposals.Create(ctx, tc.DB, permission.Kind, nil, title, payload, permission.Preview{Args: args},
		permission.Fingerprint(t.Name, tc.RunID, payload.Args), tc.Now())
	if err != nil {
		return nil, err
	}
	found, err := proposals.Get(ctx, tc.DB.Read, id)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, tool.Error("the permission request disappeared")
	}
	switch found.Status {
	case proposals.Pending, proposals.Failed:
		return nil, &tool.Park{Question: title}
	case proposals.Approved, proposals.Applied:
		return real(ctx, tc, args)
	default:
		return nil, tool.Error("the operator denied this request")
	}
}
