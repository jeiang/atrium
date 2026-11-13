package application

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
)

const (
	SweepName      application.Name = "media.sweep"
	ThumbnailsName application.Name = "media.thumbnails"
)

type SweepInput struct{}

type Swept struct {
	Selected []string
	Removed  []string
	Errors   []string
}

func DescribeSweep() application.Definition {
	return application.Describe[SweepInput, Swept](SweepName, application.Command,
		[]application.Authority{application.Operator, application.System}, application.External, true)
}

func (s *Service) Sweep(ctx context.Context, _ SweepInput) (application.Change[Swept], error) {
	out, err := s.sweep(ctx)
	failure := err
	if failure == nil && len(out.Errors) > 0 {
		failure = SweepFailure(out)
	}
	id, rerr := s.record(ctx, SweepName, "", map[string]any{"selected": len(out.Selected), "removed": len(out.Removed), "errors": len(out.Errors)}, failure)
	if err != nil {
		return application.Change[Swept]{}, rerr
	}
	return application.Change[Swept]{Data: out, AuditID: id}, nil
}

func SweepFailure(out Swept) error {
	code := application.Unavailable
	if len(out.Removed) > 0 {
		code = application.Partial
	}
	return &application.Error{Code: code, Message: fmt.Sprintf("media sweep: %d errors", len(out.Errors))}
}

func (s *Service) sweep(ctx context.Context) (Swept, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return Swept{}, err
	}
	selected, err := set.SelectFiles(ctx, s.d.Ledger, set, media.Scope{}, nil)
	if err != nil {
		return Swept{}, classify(err)
	}
	swept, err := media.Sweep(ctx, s.d.Ledger, set)
	if err != nil {
		return Swept{}, classify(err)
	}
	return Swept{Selected: selected, Removed: swept.Removed, Errors: swept.Errors}, nil
}

type ThumbnailsInput struct{}

type Thumbnails struct {
	Generated int
	Remaining int
}

func DescribeThumbnails() application.Definition {
	return application.Describe[ThumbnailsInput, Thumbnails](ThumbnailsName, application.Command,
		[]application.Authority{application.System}, application.NoEffect, false)
}

func (s *Service) Thumbnails(ctx context.Context, _ ThumbnailsInput) (application.Change[Thumbnails], error) {
	out, err := s.thumbnails(ctx)
	id, err := s.record(ctx, ThumbnailsName, "", map[string]any{"generated": out.Generated, "remaining": out.Remaining}, err)
	if err != nil {
		return application.Change[Thumbnails]{}, err
	}
	return application.Change[Thumbnails]{Data: out, AuditID: id}, nil
}

func (s *Service) thumbnails(ctx context.Context) (Thumbnails, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return Thumbnails{}, err
	}
	root, err := set.NeedRoot()
	if err != nil {
		return Thumbnails{}, classify(err)
	}
	generated, remaining := media.NewArtwork(s.d.CacheDir, nil, set.Tuning).ThumbnailPass(ctx, root, set.ThumbPassLimit)
	return Thumbnails{Generated: generated, Remaining: remaining}, nil
}
