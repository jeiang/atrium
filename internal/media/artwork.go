package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	KindShow   = "show"
	KindSeason = "season"

	postersDir = "posters"
	thumbsDir  = "thumbs"
)

type Runner func(ctx context.Context, argv []string) (stdout, stderr string, code int, err error)

var ffmpegMu sync.Mutex

type Artwork struct {
	Cache string
	TMDB  *TMDB
	Run   Runner
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
	slug := py.Strip(b.String())
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

func (a *Artwork) thumbFile(rel string) string {
	return filepath.Join(a.Cache, thumbsDir, rel+".jpg")
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
	if isFile(path) {
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

func (a *Artwork) run(ctx context.Context, argv []string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, a.FFmpegTimeout)
	defer cancel()
	if a.Run != nil {
		return a.Run(ctx, argv)
	}
	return execRun(ctx, argv)
}

func execRun(ctx context.Context, argv []string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), stderr.String(), exit.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), 0, err
}

func (a *Artwork) niced(argv ...string) []string {
	return append([]string{"nice", "-n", a.FFmpegNice}, argv...)
}

func (a *Artwork) duration(ctx context.Context, full string) (float64, error) {
	stdout, _, code, err := a.run(ctx, a.niced("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", full))
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, nil
	}
	seconds, perr := py.ParseFloat(stdout)
	if perr != nil {
		return 0, nil
	}
	return seconds, nil
}

func clip(text string, n int) string {
	runes := []rune(text)
	return string(runes[:min(n, len(runes))])
}

func seekText(at float64) string {
	if math.IsInf(at, 1) {
		return "inf"
	}
	return strconv.FormatFloat(at, 'f', 2, 64)
}

func (a *Artwork) Thumbnail(ctx context.Context, root, rel string) (string, bool) {
	full, ok := SafeRel(root, rel)
	if !ok {
		return "", false
	}
	path := a.thumbFile(rel)
	if isFile(path) {
		return path, true
	}
	if a.freshNone(path) {
		return "", false
	}
	ffmpegMu.Lock()
	defer ffmpegMu.Unlock()
	if isFile(path) {
		return path, true
	}
	fail := func(err error) (string, bool) {
		if ctx.Err() == nil {
			rememberNone(path)
		}
		log.Warn(fmt.Sprintf("thumbnail %s: %v", rel, err), logx.Err, err)
		return "", false
	}
	seconds, err := a.duration(ctx, full)
	if err != nil {
		return fail(err)
	}
	at := 0.0
	if scaled := seconds * a.ThumbPosition; scaled > 0 {
		at = scaled
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(err)
	}
	tmp := strings.TrimSuffix(path, ".jpg") + ".tmp.jpg"
	_, stderr, code, err := a.run(ctx, a.niced("ffmpeg", "-v", "error", "-y", "-ss", seekText(at), "-i", full, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2", a.ThumbWidth), "-q:v", "4", tmp))
	if err != nil {
		return fail(err)
	}
	if code != 0 || !isFile(tmp) {
		return fail(errors.New(clip(strings.TrimSpace(stderr), 200)))
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return path, true
}

func (a *Artwork) ThumbnailPass(ctx context.Context, root string, limit int) (generated, remaining int) {
	for _, rel := range a.EpisodeFiles(root) {
		if ctx.Err() != nil {
			break
		}
		if isFile(a.thumbFile(rel)) {
			continue
		}
		if generated >= limit {
			remaining++
			continue
		}
		if _, ok := a.Thumbnail(ctx, root, rel); ok {
			generated++
		}
	}
	return generated, remaining
}
