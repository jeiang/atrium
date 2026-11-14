package application

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/settings"
)

func PermissionFields(ctx context.Context) []settings.Field {
	reg := settings.From(ctx)
	out := make([]settings.Field, 0, len(registry.PermissionChannelLabels)*len(registry.PermissionSituationLabels))
	for s := range registry.PermissionSituationLabels {
		for c := range registry.PermissionChannelLabels {
			key := registry.PermissionKey(c, s)
			def, _ := reg.Lookup(key)
			out = append(out, settings.Field{Key: key, Label: registry.PermissionChannelLabels[c], Choices: def.Choices})
		}
	}
	return out
}
