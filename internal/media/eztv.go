package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/py"
)

var (
	imdbRe = regexp.MustCompile(`^(?:tt)?(\d{7,9})$`)
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
	text := strings.TrimSpace(value)
	loc := imdbRe.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", false
	}
	return text[loc[2]:loc[3]], true
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case py.Dict:
		return len(v) > 0
	}
	return true
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
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
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
	n, ok := intOf(f)
	if !ok {
		return 0
	}
	return n
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
	return fmt.Sprint(v)
}

func orEmpty(v any) string {
	if !truthy(v) {
		return ""
	}
	return pyText(v)
}

func (t Tuning) FetchReleases(ctx context.Context, imdb string) ([]Release, error) {
	target := t.EZTVURL + "?" + encodeParams([]param{{"imdb_id", imdb}, {"limit", strconv.Itoa(t.EZTVLimit)}})
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, EztvError("EZTV unreachable: invalid URL")
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
		if !truthy(hash) || !truthy(magnet) {
			continue
		}
		title, _ := t.Get("title")
		size, _ := t.Get("size_bytes")
		season, _ := t.Get("season")
		episode, _ := t.Get("episode")
		out = append(out, Release{
			Hash:      strings.ToLower(pyText(hash)),
			Title:     orEmpty(title),
			MagnetURL: pyText(magnet),
			SizeBytes: sizeOf(size),
			Season:    orEmpty(season),
			Episode:   orEmpty(episode),
		})
	}
	return out, nil
}
