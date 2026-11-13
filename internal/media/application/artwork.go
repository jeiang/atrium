package application

import (
	"context"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/media"
)

func open(path, contentType string) *FileStream {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil
	}
	return &FileStream{ReadSeekCloser: f, Name: info.Name(), ModTime: info.ModTime(), ContentType: contentType}
}

func (s *Service) Art(ctx context.Context, in ArtInput) (*FileStream, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	art := media.NewArtwork(s.d.CacheDir, set.TMDB(), set.Tuning)
	var path string
	var ok bool
	switch in.Kind {
	case media.KindMovie, media.KindShow, media.KindSeason:
		if strings.Contains(in.Name, "/") {
			return nil, notFound("not found")
		}
		path, ok = art.Poster(ctx, in.Kind, in.Name, in.Season)
	default:
		return nil, notFound("not found")
	}
	if !ok {
		return nil, nil
	}
	return open(path, "image/jpeg"), nil
}
