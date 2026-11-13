package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/logx"
)

const postersDir = "posters"

type Artwork struct {
	Cache string
	TMDB  *TMDB
	Tuning
}

func NewArtwork(cacheDir string, tmdb *TMDB, t Tuning) *Artwork {
	return &Artwork{Cache: cacheDir, TMDB: tmdb, Tuning: t}
}

func slugOf(text string) string {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune(" ()-_.", r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	slug := strings.TrimSpace(b.String())
	if slug == "" || slug == "." || slug == ".." {
		return "_"
	}
	return slug
}

func (a *Artwork) posterFile(kind, name string, season int64) string {
	base := filepath.Join(a.Cache, postersDir, kind, slugOf(name))
	if kind == KindSeason {
		return filepath.Join(base, fmt.Sprintf("%02d.jpg", season))
	}
	return withJPG(base)
}

func withJPG(path string) string {
	name := filepath.Base(path)
	if i := strings.LastIndexByte(name, '.'); i > 0 && i < len(name)-1 {
		return path[:len(path)-len(name)+i] + ".jpg"
	}
	return path + ".jpg"
}

func noneMarker(path string) string { return strings.TrimSuffix(path, ".jpg") + ".none" }

func (a *Artwork) freshNone(path string) bool {
	info, err := os.Stat(noneMarker(path))
	return err == nil && Now()-float64(info.ModTime().UnixNano())/1e9 < a.ArtworkMissTTL
}

func rememberNone(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	marker := noneMarker(path)
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	_ = f.Close()
	now := time.Now()
	_ = os.Chtimes(marker, now, now)
}

func store(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, strings.TrimSuffix(filepath.Base(path), ".jpg")+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

func (a *Artwork) posterPath(ctx context.Context, kind, name string, season int64) (string, error) {
	parsed := a.ParseRelease(name)
	title := parsed.Title
	if title == "" {
		title = name
	}
	if kind == KindMovie {
		found, err := a.TMDB.SearchMovie(ctx, title, parsed.Year)
		if err != nil || len(found) == 0 {
			return "", err
		}
		if parsed.Year != "" {
			for _, movie := range found {
				if movie.Year == parsed.Year {
					return movie.PosterPath, nil
				}
			}
		}
		return found[0].PosterPath, nil
	}
	shows, err := a.TMDB.SearchTV(ctx, title, a.ShowSearchLimit)
	if err != nil {
		return "", err
	}
	if parsed.Year != "" {
		var same []TVResult
		for _, s := range shows {
			if s.Year == parsed.Year {
				same = append(same, s)
			}
		}
		if len(same) > 0 {
			shows = same
		}
	}
	if len(shows) == 0 {
		return "", nil
	}
	if kind == KindShow {
		return shows[0].PosterPath, nil
	}
	detail, err := a.TMDB.Season(ctx, shows[0].TmdbID, season)
	if err != nil {
		return "", err
	}
	return textOf(detail, "poster_path"), nil
}

func (a *Artwork) Poster(ctx context.Context, kind, name string, season int64) (string, bool) {
	path := a.posterFile(kind, name, season)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, true
	}
	if a.TMDB == nil || a.freshNone(path) {
		return "", false
	}
	poster, err := a.posterPath(ctx, kind, name, season)
	if err != nil {
		log.Warn(fmt.Sprintf("poster %s %s: %v", kind, name, err), logx.Err, err)
		return "", false
	}
	if poster == "" {
		rememberNone(path)
		return "", false
	}
	data, err := a.TMDB.Image(ctx, poster)
	if err == nil {
		err = store(path, data)
	}
	if err != nil {
		log.Warn(fmt.Sprintf("poster %s %s: %v", kind, name, err), logx.Err, err)
		return "", false
	}
	return path, true
}
