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

const KindQbittorrent connections.Kind = "service/qbittorrent"

func init() {
	connections.Register(connections.Definition{
		Kind:     KindQbittorrent,
		Validate: validateQbitConnection,
		Test:     testQbitConnection,
	})
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
	client, err := lease.HTTP("url")
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
