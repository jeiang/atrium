package mail

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/endpoint"
)

func EmbedIndexes(ctx context.Context, d *db.DB) ([]embed.Index, error) {
	active, err := embed.Active(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	building, ok, err := embed.Building(ctx, d)
	if err != nil || !ok {
		return []embed.Index{active}, err
	}
	return []embed.Index{active, building.Index}, nil
}

func ReembedStep(ctx context.Context, d *db.DB) (bool, error) {
	e, err := endpoint.OpenEmbedding(ctx, d.Read)
	if err != nil {
		if endpoint.IsUnconfigured(err) {
			return false, nil
		}
		return false, err
	}
	defer e.Close()
	ctx = endpoint.WithEmbedding(ctx, e)
	s := endpoint.Settings{EmbedBodyChars: endpoint.TuningMailEmbedBodyChars, Batch: endpoint.TuningMailEmbedBatch}
	return embed.Rebuild(ctx, d, e.Base, e.Model, s, func(ctx context.Context, _, _ string) (string, int) {
		return e.Probe(ctx)
	})
}
