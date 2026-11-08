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
