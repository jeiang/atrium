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
	endpoints, err := endpoint.Endpoints(ctx, d.Read)
	if err != nil {
		return false, err
	}
	target, ok := endpoint.Of(endpoints, endpoint.EndpointEmbed)
	if !ok || target.URL == "" {
		return false, nil
	}
	ctx, err = endpoint.WithEndpoints(ctx, d.Read, []endpoint.Endpoint{target})
	if err != nil {
		return false, err
	}
	r := endpoint.Read(ctx, d.Read)
	s := endpoint.Settings{EmbedBodyChars: r.Int(endpoint.KeyEmbedBodyChars), Batch: r.Int(endpoint.KeyEmbedBatch)}
	if r.Err() != nil {
		return false, r.Err()
	}
	return embed.Rebuild(ctx, d, target.URL, target.Model, s, func(ctx context.Context, url, model string) (string, int) {
		return probeEmbed(ctx, endpoint.Endpoint{Kind: endpoint.EndpointEmbed, URL: url, Model: model})
	})
}
