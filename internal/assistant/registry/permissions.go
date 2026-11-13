package registry

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
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

func PromptCarriesKnowledge(kind tool.Kind) bool { return kind != tool.Sub && kind != tool.Worker }

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

func Settings() []settings.Def {
	table := DefaultTable()
	var out []settings.Def
	for i := range channelKeys {
		for s := range situationKeys {
			choices := []string{"allow", "ask", "deny"}
			if (i == 2 || i == 3) && (s == 3 || s == 4) {
				choices = []string{"ask", "deny"}
			}
			out = append(out, settings.Value(settings.UI, settings.Def{
				Key: PermissionKey(i, s), Module: "permissions", Type: settings.Enum,
				Choices: choices, Default: decisionNames[table[i][s]],
			}))
		}
	}
	return out
}

func PermissionAxes() (channels, situations []string) { return channelKeys[:], situationKeys[:] }

func PermissionFloor(channel, situation int) Decision { return floors[channel][situation] }

func (d Decision) String() string { return decisionNames[d] }

func ParseDecision(value string) (Decision, bool) {
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
			decision, ok := ParseDecision(found.Value)
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
