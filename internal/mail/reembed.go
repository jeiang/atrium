package mail

import (
	"context"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/httpx"
)

func EmbedIndexes(ctx context.Context, q Querier) ([]embed.Index, error) {
	active, err := embed.Active(ctx, q)
	if err != nil {
		return nil, err
	}
	building, ok, err := embed.Building(ctx, q)
	if err != nil || !ok {
		return []embed.Index{active}, err
	}
	return []embed.Index{active, building}, nil
}

func ReembedStep(ctx context.Context, d *db.DB, now float64) (bool, error) {
	endpoints, err := Endpoints(ctx, d.Read)
	if err != nil {
		return false, err
	}
	target := endpoints[slices.IndexFunc(endpoints, func(e Endpoint) bool { return e.Kind == EndpointEmbed })]
	if target.URL == "" {
		return false, nil
	}
	active, err := embed.Active(ctx, d.Read)
	if err != nil {
		return false, err
	}
	if active.URL == "" {
		return false, embed.Adopt(ctx, d, target.URL, target.Model)
	}
	serves := func(i embed.Index) bool { return i.URL == target.URL && i.Model == target.Model }
	building, ok, err := embed.Building(ctx, d.Read)
	if err != nil {
		return false, err
	}
	if ok && !serves(building) {
		if err := embed.Discard(ctx, d, building); err != nil {
			return false, err
		}
		ok = false
	}
	if serves(active) {
		return false, nil
	}
	ctx, err = withEndpoints(ctx, d.Read, []Endpoint{target})
	if err != nil {
		return false, err
	}
	if !ok {
		reason, dim := probeEmbed(ctx, target)
		if reason != "" {
			return false, errors.New(reason)
		}
		if building, err = embed.Create(ctx, d, target.URL, target.Model, dim, now); err != nil {
			return false, err
		}
	}
	r := Read(ctx, d.Read)
	opts := embed.Options{Model: target.Model, BodyChars: r.Int(KeyEmbedBodyChars), Batch: r.Int(KeyEmbedBatch), Dim: building.Dimension}
	if r.Err() != nil {
		return false, r.Err()
	}
	fill := embed.WithOptions(ctx, opts)
	n, err := embed.FillMessages(fill, d, target.URL, building)
	if err == nil && n == 0 {
		n, err = embed.FillNotion(fill, d, target.URL, building)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	failure := ""
	if err != nil {
		failure = httpx.Message(err)
	}
	if rerr := embed.Record(context.WithoutCancel(ctx), d, building, failure); rerr != nil {
		return false, rerr
	}
	if err != nil || n > 0 {
		return n > 0, err
	}
	_, err = embed.Swap(ctx, d, building, now)
	return false, err
}
