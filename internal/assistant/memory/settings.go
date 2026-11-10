package memory

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	NodeBytesKey   = "memory_node_bytes"
	ModelKey       = "memory_model"
	EffortKey      = "memory_effort"
	ConcurrencyKey = "memory_concurrency"
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: NodeBytesKey, Module: "memory", Type: settings.Int, Default: "512",
		Min: 64, Max: 8192, Check: settings.Between(64, 8192),
	}))
	settings.MustRegister(settings.Value(settings.UI, settings.Def{
		Key: ModelKey, Label: "Memory summary model", Module: "memory", Type: settings.String, Default: "claude-haiku-5-5",
	}))
	settings.MustRegister(settings.Value(settings.UI, settings.Def{
		Key: EffortKey, Label: "Memory summary effort", Module: "memory", Type: settings.String, Default: "low",
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: ConcurrencyKey, Module: "memory", Type: settings.Int, Default: "4",
		Min: 1, Max: 16, Check: settings.Between(1, 16),
	}))
}

const (
	ViewLowKey        = "memory_view_low"
	ViewHighKey       = "memory_view_high"
	TailMessagesKey   = "memory_tail_messages"
	VerbatimTokensKey = "memory_verbatim_tokens"
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: ViewLowKey, Module: "memory", Type: settings.Int, Default: "64000",
		Min: 1024, Max: 1000000, Check: settings.Between(1024, 1000000),
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: ViewHighKey, Module: "memory", Type: settings.Int, Default: "128000",
		Min: 2048, Max: 2000000, Check: settings.Between(2048, 2000000),
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: TailMessagesKey, Module: "memory", Type: settings.Int, Default: "6",
		Min: 0, Max: 100, Check: settings.Between(0, 100),
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: VerbatimTokensKey, Module: "memory", Type: settings.Int, Default: "16000",
		Min: 0, Max: 200000, Check: settings.Between(0, 200000),
	}))
}

func VerbatimTokens(ctx context.Context, q db.Querier) (int64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, VerbatimTokensKey)
	if err != nil {
		return 0, err
	}
	return found.Int()
}
