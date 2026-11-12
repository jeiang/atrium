package memory

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ModelKey  = "memory_model"
	EffortKey = "memory_effort"

	VerbatimTokensKey = "memory_verbatim_tokens"
)

func Settings() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: ModelKey, Label: "Memory summary model", Module: "memory", Type: settings.String, Default: "claude-haiku-5-5",
		}),
		settings.Value(settings.UI, settings.Def{
			Key: EffortKey, Label: "Memory summary effort", Module: "memory", Type: settings.String, Default: "low",
		}),
		settings.Value(settings.Advanced, settings.Def{
			Key: VerbatimTokensKey, Module: "memory", Type: settings.Int, Default: "16000",
			Min: 0, Max: 200000, Check: settings.Between(0, 200000),
		}),
	}
}

func VerbatimTokens(ctx context.Context, q db.Querier) (int64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, VerbatimTokensKey)
	if err != nil {
		return 0, err
	}
	return found.Int()
}
