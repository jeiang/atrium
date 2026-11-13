package application

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
)

func (s *Service) OperatorFile(ctx context.Context, in FileInput) (*FileRelay, error) {
	root, rest, ok := media.SplitRel(in.Rel)
	if !ok {
		return nil, notFound("not found")
	}
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot(ctx, set)
	if err != nil {
		if errors.As(err, new(config.NotConfigured)) {
			return nil, notFound("not found")
		}
		return nil, classify(err)
	}
	name, _, _ := strings.Cut(rest, "/")
	found := false
	if root == media.TV {
		show, err := set.Show(snap, name)
		if err != nil {
			return nil, notFound("not found")
		}
		for _, season := range show.Seasons {
			for _, file := range season.Episodes {
				found = found || file.Rel == in.Rel
			}
		}
	} else {
		movie, err := set.Movie(snap, name)
		if err != nil {
			return nil, notFound("not found")
		}
		for _, file := range movie.Files {
			found = found || file.Rel == in.Rel
		}
	}
	if !found {
		return nil, notFound("not found")
	}
	signed, err := s.d.Links.PublicURL(ctx, "", in.Rel, s.d.Now(), set.LinkTTL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		return nil, err
	}
	in.Exp, in.Sig = parsed.Query().Get("exp"), parsed.Query().Get("sig")
	return s.File(ctx, in)
}
