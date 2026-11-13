package application

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
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
	if failure != nil && len(out.Selected)+len(out.Removed) > 0 {
		failure = &application.Error{Code: application.Partial, Message: "Media sweep stopped after completing some work.", Cause: failure}
		if err != nil {
			err = failure
		}
	}
	outcome := state.OK
	if len(out.Selected)+len(out.Removed) == 0 {
		outcome = state.Noop
	}
	id, _ := s.recordOutcome(ctx, SweepName, "", map[string]any{"selected": len(out.Selected), "removed": len(out.Removed), "errors": len(out.Errors)}, outcome, failure)
	return application.Change[Swept]{Data: out, AuditID: id}, err
}

func SweepFailure(out Swept) error {
	code := application.Unavailable
	if len(out.Selected)+len(out.Removed) > 0 {
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
	out := Swept{Selected: selected}
	if err != nil {
		return out, classify(err)
	}
	swept, err := media.Sweep(ctx, s.d.Ledger, set)
	out.Removed, out.Errors = swept.Removed, swept.Errors
	return out, classify(err)
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
	outcome := state.OK
	if out.Generated == 0 {
		outcome = state.Noop
	}
	id, err := s.recordOutcome(ctx, ThumbnailsName, "", map[string]any{"generated": out.Generated, "remaining": out.Remaining}, outcome, err)
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
	snap, err := s.snapshot(ctx, set)
	if err != nil {
		return Thumbnails{}, classify(err)
	}
	client, err := set.Agent(protocol.CapabilityFile)
	if err != nil {
		return Thumbnails{}, classify(err)
	}
	paths := []protocol.Path{}
	for _, rel := range set.EpisodeFiles(snap) {
		if root, rest, ok := media.SplitRel(rel); ok {
			paths = append(paths, protocol.Path{Root: root, Rel: rest})
		}
	}
	var out Thumbnails
	for len(paths) > 0 {
		batch := paths[:min(len(paths), protocol.MaxThumbnailPaths)]
		paths = paths[len(batch):]
		result, err := client.ThumbnailPass(ctx, protocol.ThumbnailPass{Paths: batch, Limit: max(set.ThumbPassLimit-out.Generated, 0)})
		if err != nil {
			return out, &media.LibraryUnavailable{Cause: err}
		}
		out.Generated += result.Generated
		out.Remaining += result.Remaining
	}
	return out, nil
}
