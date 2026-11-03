package loop

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	CacheTTLKey = "cache_ttl_chat"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	settings.MustRegister(
		settings.Def{
			Key: CacheTTLKey, Module: "assistant", Type: settings.Enum, Default: prompts.TTL5m,
			Choices: []string{prompts.TTL5m, prompts.TTL1h}, Class: settings.UI, Scopes: instance,
		},
	)
}

func cacheTTL(ctx context.Context, q db.Querier) (string, error) {
	found, err := settings.Default.Resolve(ctx, q, CacheTTLKey, 0)
	return found.Value, err
}

func readWebSearch(ctx context.Context, q db.Querier) (registry.WebSearch, error) {
	r := assistant.Read(ctx, q)
	web := registry.WebSearch{MaxUses: r.Int(assistant.KeyWebSearchMaxUses), DynamicFiltering: r.Text(assistant.KeyWebSearchDynamicFiltering) == "true"}
	if err := r.Err(); err != nil {
		return web, err
	}
	var err error
	web.Enabled, err = registry.ToolEnabled(ctx, q, registry.WebSearchName)
	return web, err
}
