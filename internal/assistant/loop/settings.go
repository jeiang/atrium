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
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: CacheTTLKey, Module: "assistant", Type: settings.Enum, Default: prompts.TTL5m,
			Choices: []string{prompts.TTL5m, prompts.TTL1h}}),
	)
}

func cacheTTL(ctx context.Context, q db.Querier) (string, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, CacheTTLKey)
	return found.Value, err
}

func readWebSearchMaxUses(_ context.Context, _ db.Querier) (int, error) {
	return assistant.TuningAgentWebSearchMaxUses, nil
}

func readWebSearchDynamicFiltering(_ context.Context, _ db.Querier) (bool, error) {
	return registry.TuningSearchWebDynamicFiltering, nil
}
