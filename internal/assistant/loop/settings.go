package loop

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	CacheTTLKey   = "cache_ttl_chat"
	MaxStepsKey   = "max_steps"
	MaxRunCostKey = "max_run_cost_usd"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	settings.MustRegister(
		settings.Def{
			Key: CacheTTLKey, Module: "assistant", Type: settings.Enum, Default: prompts.TTL5m,
			Choices: []string{prompts.TTL5m, prompts.TTL1h}, Class: settings.UI, Scopes: instance,
		},
		settings.Def{
			Key: MaxStepsKey, Module: "assistant", Type: settings.Int, Default: "24",
			Check: settings.Between(1, 200), Class: settings.Advanced, Scopes: instance,
		},
		settings.Def{
			Key: MaxRunCostKey, Module: "assistant", Type: settings.Float, Default: "2",
			Check: settings.Above(0), Class: settings.Cap, Scopes: instance,
		},
	)
}

func cacheTTL(ctx context.Context, q db.Querier) (string, error) {
	found, err := settings.Default.Resolve(ctx, q, CacheTTLKey, 0)
	return found.Value, err
}

func maxSteps(ctx context.Context, q db.Querier) (int, error) {
	found, err := settings.Default.Resolve(ctx, q, MaxStepsKey, 0)
	if err != nil {
		return 0, err
	}
	n, err := found.Int()
	return int(n), err
}

func maxRunCost(ctx context.Context, q db.Querier) (float64, error) {
	found, err := settings.Default.Resolve(ctx, q, MaxRunCostKey, 0)
	if err != nil {
		return 0, err
	}
	return found.Float()
}
