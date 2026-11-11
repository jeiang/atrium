package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"regexp"

	"github.com/jeiang/atrium/internal/config"
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

func (s Settings) FetchReleases(ctx context.Context, imdb string) ([]Release, error) {
	if s.EZTVLease == nil {
		return nil, config.NotConfigured("the EZTV connection is not configured")
	}
	base, err := s.EZTVLease.Endpoint("url")
	if err != nil {
		return nil, config.NotConfigured("the EZTV connection has no URL")
	}
	client, err := s.EZTVLease.HTTPRead("url")
	if err != nil {
		return nil, err
	}
	target := base + "?" + encodeParams([]param{{"imdb_id", imdb}, {"limit", strconv.Itoa(s.EZTVLimit)}})
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, EztvError("EZTV unreachable: invalid URL")
	}
	res, err := send(ctx, client, req)
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
	var body map[string]any
	if err := json.Unmarshal(res.body, &body); err != nil || body == nil {
		return nil, EztvError("EZTV: not JSON")
	}
	list, _ := body["torrents"].([]any)
	out := []Release{}
	for _, item := range list {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hash, magnet := t["hash"], t["magnet_url"]
		if !truthy(hash) || !truthy(magnet) {
			continue
		}
		title, size := t["title"], t["size_bytes"]
		season, episode := t["season"], t["episode"]
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
