package application

import (
	"context"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/media"
)

func (s *Service) OperatorFile(ctx context.Context, in FileInput) (*FileRelay, error) {
	root, rest, ok := media.SplitRel(in.Rel)
	if !ok {
		return nil, notFound("not found")
	}
	kind := media.KindMovie
	if root == media.TV {
		kind = media.KindShow
	}
	name, _, _ := strings.Cut(rest, "/")
	detail, err := s.LibraryDetail(ctx, LibraryDetailInput{Kind: kind, Name: name})
	if err != nil {
		return nil, err
	}
	found := false
	for _, file := range detail.Data.Files {
		found = found || file.Rel == in.Rel
	}
	for _, season := range detail.Data.Seasons {
		for _, file := range season.Episodes {
			found = found || file.Rel == in.Rel
		}
	}
	if !found {
		return nil, notFound("not found")
	}
	signed, err := s.d.Links.PublicURL(ctx, "", in.Rel, s.d.Now(), detail.Data.LinkTTL)
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
