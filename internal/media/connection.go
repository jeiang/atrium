package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
)

const (
	KindQbittorrent connections.Kind = "service/qbittorrent"
	KindTMDB        connections.Kind = "service/tmdb"
	KindEZTV        connections.Kind = "service/eztv"
	KindMediaAgent  connections.Kind = "service/media-agent"
)

const eztvProbeIMDb = "tt0000001"

func Connections() []connections.Definition {
	return []connections.Definition{
		{Kind: KindMediaAgent, Validate: validateMediaAgent, Test: testMediaAgent},
		{
			Kind:     KindQbittorrent,
			Validate: validateQbitConnection,
			Test:     testQbitConnection,
		},
		{
			Kind:     KindTMDB,
			Validate: validateTMDBConnection,
			Test:     testTMDBConnection,
		},
		{
			Kind:     KindEZTV,
			Validate: validateEZTVConnection,
			Test:     testEZTVConnection,
		},
	}
}

type qbitConfig struct {
	URL string `json:"url"`
}

func validateQbitConnection(c connections.Candidate) error {
	var cfg qbitConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("qBittorrent config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("qBittorrent needs a URL")
	}
	return requiredHTTPURL(cfg.URL)
}

func testQbitConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	client, err := lease.HTTPRead("url")
	if err != nil {
		return nil, err
	}
	token, _ := lease.Credential("api_key")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v2/app/version", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	}
	version := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: fmt.Sprintf("qBittorrent answered %d", resp.StatusCode)}}, nil
	case resp.StatusCode != http.StatusOK:
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: fmt.Sprintf("qBittorrent answered %d", resp.StatusCode)}}, nil
	case token != "" && !apiKeys(version):
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: fmt.Sprintf("qBittorrent %s has no API keys; upgrade to 5.2", version)}}, nil
	}
	data, err := json.Marshal(map[string]string{"version": version})
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "api", State: connections.OK, Data: data}}, nil
}

func apiKeys(version string) bool {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && (major > 5 || major == 5 && minor >= 2)
}

type tmdbConfig struct {
	URL       string `json:"url"`
	ImagesURL string `json:"images_url"`
}

func validateTMDBConnection(c connections.Candidate) error {
	var cfg tmdbConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("TMDB config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("TMDB needs an API URL")
	}
	if err := requiredHTTPURL(cfg.URL); err != nil {
		return fmt.Errorf("TMDB API URL %w", err)
	}
	if strings.TrimSpace(cfg.ImagesURL) == "" {
		return fmt.Errorf("TMDB needs an image URL")
	}
	if err := requiredHTTPURL(cfg.ImagesURL); err != nil {
		return fmt.Errorf("TMDB image URL %w", err)
	}
	return nil
}

func testTMDBConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	client, err := lease.HTTPRead("url")
	if err != nil {
		return nil, err
	}
	token, err := lease.Credential("read_token")
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/configuration", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: "TMDB refused the read token"}}, nil
	case resp.StatusCode != http.StatusOK:
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: fmt.Sprintf("TMDB answered %d", resp.StatusCode)}}, nil
	}
	images, err := testTMDBImages(ctx, lease)
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "api", State: connections.OK}, images}, nil
}

func testTMDBImages(ctx context.Context, lease *connections.Lease) (connections.Result, error) {
	base, err := lease.Endpoint("images_url")
	if err != nil {
		return connections.Result{Capability: "images", State: connections.Failing, Reason: err.Error()}, nil
	}
	client, err := lease.HTTPRead("images_url")
	if err != nil {
		return connections.Result{Capability: "images", State: connections.Failing, Reason: err.Error()}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return connections.Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return connections.Result{Capability: "images", State: connections.Failing, Reason: err.Error()}, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return connections.Result{Capability: "images", State: connections.Failing, Reason: fmt.Sprintf("the image host answered %d", resp.StatusCode)}, nil
	}
	return connections.Result{Capability: "images", State: connections.OK}, nil
}

type eztvConfig struct {
	URL string `json:"url"`
}

func validateEZTVConnection(c connections.Candidate) error {
	var cfg eztvConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("EZTV config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("EZTV needs a URL")
	}
	return requiredHTTPURL(cfg.URL)
}

func testEZTVConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	client, err := lease.HTTPRead("url")
	if err != nil {
		return nil, err
	}
	target := base + "?" + encodeParams([]param{{"imdb_id", eztvProbeIMDb}, {"limit", "1"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return []connections.Result{{Capability: "indexer", State: connections.Failing, Reason: err.Error()}}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return []connections.Result{{Capability: "indexer", State: connections.Failing, Reason: fmt.Sprintf("EZTV answered %d", resp.StatusCode)}}, nil
	}
	var body struct {
		Torrents []json.RawMessage `json:"torrents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return []connections.Result{{Capability: "indexer", State: connections.Failing, Reason: "EZTV did not answer with its torrent list"}}, nil
	}
	if body.Torrents == nil {
		return []connections.Result{{Capability: "indexer", State: connections.Failing, Reason: "EZTV did not answer with its torrent list"}}, nil
	}
	return []connections.Result{{Capability: "indexer", State: connections.OK}}, nil
}
