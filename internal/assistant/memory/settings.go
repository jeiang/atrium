package memory

import (
	"github.com/jeiang/atrium/internal/settings"
)

const NodeBytesKey = "node_bytes"

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: NodeBytesKey, Module: "memory", Type: settings.Int, Default: "512",
		Min: 64, Max: 8192, Check: settings.Between(64, 8192),
	}))
}
