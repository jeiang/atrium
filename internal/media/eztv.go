package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var (
	imdbRe = rx.MustCompile(`^(?:tt)?(\d{7,9})$`, 0)
)

type EztvError string

func (e EztvError) Error() string { return string(e) }

type Release struct {
	Hash      string
	Title     string
	MagnetURL string
	SizeBytes int64
	Season    string
	Episode   string
}

func IMDBDigits(value string) (string, bool) {
	m := matched(imdbRe.Search(py.Strip(value)))
	if m == nil {
		return "", false
	}
	return m.Group(1), true
}

func sizeOf(v any) int64 {
	var f float64
	switch v := v.(type) {
	case nil:
		return 0
	case string:
		if v == "" {
			return 0
		}
		parsed, err := py.ParseFloat(v)
		if err != nil {
			return 0
		}
		f = parsed
	case bool:
		if v {
			return 1
		}
		return 0
	case int64:
		return v
	case float64:
		f = v
	default:
		return 0
	}
	n, err := py.Int(f)
	if err != nil || !n.IsInt64() {
		return 0
	}
	return n.Int64()
}

func pyText(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return py.ReprValue(v)
}

func orEmpty(v any) string {
	if !py.Truthy(v) {
		return ""
	}
	return pyText(v)
}

func (t Tuning) FetchReleases(ctx context.Context, imdb string) ([]Release, error) {
	target := t.EZTVURL + "?" + encodeParams([]param{{"imdb_id", imdb}, {"limit", strconv.Itoa(t.EZTVLimit)}})
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, EztvError("EZTV unreachable: InvalidURL")
	}
	res, err := send(ctx, clientFor(t.EZTVTimeout), req)
	if err != nil {
		var failure *httpFailure
		if errors.As(err, &failure) {
			return nil, EztvError("EZTV unreachable: " + failure.class)
		}
		return nil, err
	}
	if res.status != http.StatusOK {
		return nil, EztvError(fmt.Sprintf("EZTV: %d", res.status))
	}
	parsed, err := py.JSONLoads(res.body)
	body, isDict := parsed.(py.Dict)
	if err != nil || !isDict {
		return nil, EztvError("EZTV: not JSON")
	}
	torrents, _ := body.Get("torrents")
	list, _ := torrents.([]any)
	out := []Release{}
	for _, item := range list {
		t, ok := item.(py.Dict)
		if !ok {
			continue
		}
		hash, _ := t.Get("hash")
		magnet, _ := t.Get("magnet_url")
		if !py.Truthy(hash) || !py.Truthy(magnet) {
			continue
		}
		title, _ := t.Get("title")
		size, _ := t.Get("size_bytes")
		season, _ := t.Get("season")
		episode, _ := t.Get("episode")
		out = append(out, Release{
			Hash:      py.Lower(pyText(hash)),
			Title:     orEmpty(title),
			MagnetURL: pyText(magnet),
			SizeBytes: sizeOf(size),
			Season:    orEmpty(season),
			Episode:   orEmpty(episode),
		})
	}
	return out, nil
}
