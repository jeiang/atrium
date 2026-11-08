package policy

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	EnabledKey = "phone_actions_enabled"
	KindsKey   = "phone_actions_kinds"
)

var DefaultKinds = []string{"reminder", "triage_label", "triage_archive", "triage_snooze", "memory", "profile"}

var WebOnly = map[string]bool{
	"gmail_label_merge": true,
	"inbox_backlog":     true,
	"rebalance_split":   true,
	"rebalance_merge":   true,
	"deletion_rule":     true,
}

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{Key: EnabledKey, Module: "action_links", Type: settings.Bool, Default: "false"}),
		settings.Value(settings.UI, settings.Def{Key: KindsKey, Module: "action_links", Type: settings.String, Default: strings.Join(DefaultKinds, ",")}),
	)
}

type Policy struct {
	Enabled bool
	Kinds   map[string]bool
}

func Load(ctx context.Context, q db.Querier) (Policy, error) {
	on, err := settings.From(ctx).Resolve(ctx, q, EnabledKey)
	if err != nil {
		return Policy{}, err
	}
	kinds, err := settings.From(ctx).Resolve(ctx, q, KindsKey)
	if err != nil {
		return Policy{}, err
	}
	p := Policy{Enabled: on.Value == "true", Kinds: map[string]bool{}}
	for _, kind := range strings.Split(kinds.Value, ",") {
		kind = strings.TrimSpace(kind)
		if kind != "" && !WebOnly[kind] {
			p.Kinds[kind] = true
		}
	}
	return p, nil
}

func (p Policy) Allows(kind string) bool {
	return p.Enabled && p.Kinds[kind] && !WebOnly[kind]
}
