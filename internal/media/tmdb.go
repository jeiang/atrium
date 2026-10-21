package media

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/py"
)

var (
	TMDBAPI    = "https://api.themoviedb.org/3"
	TMDBImages = "https://image.tmdb.org/t/p/w342"
)

type TmdbError string

func (e TmdbError) Error() string { return string(e) }

type TMDB struct {
	key     string
	timeout time.Duration
}

type TVResult struct {
	TmdbID     int64
	Name       string
	Year       string
	PosterPath string
}

type MovieResult struct {
	PosterPath string
}

func NewTMDB(key string, timeout time.Duration) *TMDB {
	if key == "" {
		return nil
	}
	return &TMDB{key: key, timeout: timeout}
}

func (t *TMDB) get(ctx context.Context, path string, params ...param) (any, error) {
	target := TMDBAPI + "/" + path + "?" + encodeParams(append([]param{{"api_key", t.key}}, params...))
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, TmdbError("TMDB unreachable: InvalidURL")
	}
	res, err := send(ctx, clientFor(t.timeout), req)
	if err != nil {
		var failure *httpFailure
		if errors.As(err, &failure) {
			return nil, TmdbError("TMDB unreachable: " + failure.class)
		}
		return nil, err
	}
	if res.status == http.StatusNotFound {
		return nil, nil
	}
	if res.status != http.StatusOK {
		return nil, TmdbError("TMDB: " + strconv.Itoa(res.status))
	}
	parsed, err := py.JSONLoads(res.body)
	if err != nil {
		return nil, TmdbError("TMDB: not JSON")
	}
	return parsed, nil
}

func (t *TMDB) getDict(ctx context.Context, path string, params ...param) (py.Dict, error) {
	body, err := t.get(ctx, path, params...)
	if err != nil {
		return nil, err
	}
	dict, _ := body.(py.Dict)
	return dict, nil
}

func listOf(d py.Dict, key string) []py.Dict {
	v, _ := d.Get(key)
	items, _ := v.([]any)
	out := make([]py.Dict, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(py.Dict); ok {
			out = append(out, entry)
		}
	}
	return out
}

func textOf(d py.Dict, key string) string {
	v, _ := d.Get(key)
	s, _ := v.(string)
	return s
}

func yearOf(date string) string {
	runes := []rune(date)
	return string(runes[:min(4, len(runes))])
}

func idOf(d py.Dict) (int64, bool) {
	v, _ := d.Get("id")
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		i, err := py.Int(n)
		if err != nil || !i.IsInt64() {
			return 0, false
		}
		return i.Int64(), true
	}
	return 0, false
}

func tvResult(d py.Dict) (TVResult, bool) {
	id, ok := idOf(d)
	if !ok {
		return TVResult{}, false
	}
	return TVResult{TmdbID: id, Name: textOf(d, "name"), Year: yearOf(textOf(d, "first_air_date")), PosterPath: textOf(d, "poster_path")}, true
}

func (t *TMDB) SearchTV(ctx context.Context, query string, limit int) ([]TVResult, error) {
	body, err := t.getDict(ctx, "search/tv", param{"query", query})
	if err != nil {
		return nil, err
	}
	found := listOf(body, "results")
	out := []TVResult{}
	for _, r := range found[:min(limit, len(found))] {
		if show, ok := tvResult(r); ok {
			out = append(out, show)
		}
	}
	return out, nil
}

func (t *TMDB) SearchMovie(ctx context.Context, query, year string) ([]MovieResult, error) {
	params := []param{{"query", query}}
	if year != "" {
		params = append(params, param{"year", year})
	}
	body, err := t.getDict(ctx, "search/movie", params...)
	if err != nil {
		return nil, err
	}
	out := []MovieResult{}
	for _, r := range listOf(body, "results") {
		out = append(out, MovieResult{PosterPath: textOf(r, "poster_path")})
	}
	return out, nil
}

func (t *TMDB) ImdbOf(ctx context.Context, tmdbID int64) (string, bool, error) {
	ids, err := t.getDict(ctx, "tv/"+strconv.FormatInt(tmdbID, 10)+"/external_ids")
	if err != nil {
		return "", false, err
	}
	digits, ok := IMDBDigits(textOf(ids, "imdb_id"))
	return digits, ok, nil
}

func (t *TMDB) ShowByImdb(ctx context.Context, imdb string) (*TVResult, error) {
	body, err := t.getDict(ctx, "find/tt"+imdb, param{"external_source", "imdb_id"})
	if err != nil {
		return nil, err
	}
	found := listOf(body, "tv_results")
	if len(found) == 0 {
		return nil, nil
	}
	show, ok := tvResult(found[0])
	if !ok {
		return nil, nil
	}
	return &show, nil
}

func (t *TMDB) Show(ctx context.Context, tmdbID int64) (py.Dict, error) {
	return t.getDict(ctx, "tv/"+strconv.FormatInt(tmdbID, 10))
}

func (t *TMDB) Season(ctx context.Context, tmdbID, number int64) (py.Dict, error) {
	return t.getDict(ctx, "tv/"+strconv.FormatInt(tmdbID, 10)+"/season/"+strconv.FormatInt(number, 10))
}

func (t *TMDB) Image(ctx context.Context, posterPath string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, TMDBImages+posterPath, nil)
	if err != nil {
		return nil, TmdbError("TMDB images unreachable: InvalidURL")
	}
	res, err := send(ctx, clientFor(t.timeout), req)
	if err != nil {
		var failure *httpFailure
		if errors.As(err, &failure) {
			return nil, TmdbError("TMDB images unreachable: " + failure.class)
		}
		return nil, err
	}
	if res.status != http.StatusOK {
		return nil, TmdbError("TMDB images: " + strconv.Itoa(res.status))
	}
	return res.body, nil
}
