package embed

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
)

type Probe func(ctx context.Context, url, model string) (reason string, dim int)

func Rebuild(ctx context.Context, d *db.DB, url, model string, s endpoint.Settings, probe Probe) (bool, error) {
	active, err := Active(ctx, d.Read)
	if err != nil {
		return false, err
	}
	serves := func(i Index) bool { return i.URL == url && i.Model == model }
	build, ok, err := Building(ctx, d)
	if err != nil {
		return false, err
	}
	if ok && !serves(build.Index) {
		if err := build.Discard(ctx); err != nil {
			return false, err
		}
		ok = false
	}
	if serves(active) {
		return false, nil
	}
	if !ok {
		reason, dim := probe(ctx, url, model)
		if reason != "" {
			return false, errors.New(reason)
		}
		if active.URL == "" && dim == active.Dimension {
			return false, active.Adopt(ctx, d, url, model)
		}
		if build, err = Create(ctx, d, url, model, dim); err != nil {
			return false, err
		}
	}
	s.EmbedModel, s.Dim = model, build.Dimension
	ctx = endpoint.With(ctx, s)
	n, err := Fill(ctx, d, url, build.Index, nil)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	failure := ""
	if err != nil {
		failure = httpx.Message(err)
	}
	if rerr := build.Record(context.WithoutCancel(ctx), failure); rerr != nil {
		return false, rerr
	}
	if err != nil || n > 0 {
		return n > 0, err
	}
	_, err = build.Swap(ctx)
	return false, err
}
