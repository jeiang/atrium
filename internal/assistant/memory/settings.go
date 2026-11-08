package memory

import (
	"github.com/jeiang/atrium/internal/settings"
)

const (
	NodeBytesKey   = "node_bytes"
	ModelKey       = "model"
	EffortKey      = "effort"
	ConcurrencyKey = "concurrency"
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: NodeBytesKey, Module: "memory", Type: settings.Int, Default: "512",
		Min: 64, Max: 8192, Check: settings.Between(64, 8192),
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: ModelKey, Module: "memory", Type: settings.String, Default: "claude-haiku-5-5",
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: EffortKey, Module: "memory", Type: settings.String, Default: "low",
	}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: ConcurrencyKey, Module: "memory", Type: settings.Int, Default: "4",
		Min: 1, Max: 16, Check: settings.Between(1, 16),
	}))
}

const (
	ViewLowKey      = "view_low"
	ViewHighKey     = "view_high"
	TailMessagesKey = "tail_messages"
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
}
