package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "qbittorrent", Label: "qBittorrent", Module: module, URLKey: KeyQbitURL, SecretKey: KeyQbitAPIKey,
		Test: testQbit,
	})
	integrations.Register(integrations.Integration{
		Name: "tmdb", Label: "TMDB", Module: module, SecretKey: KeyTMDBToken,
		Test: testTMDB,
	})
}

func testQbit(ctx context.Context, c integrations.Conn) (string, error) {
	status, body, err := bearerGet(ctx, c, c.URL+"/api/v2/app/version")
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", fmt.Errorf("%w: qBittorrent answered %d", integrations.ErrAuth, status)
	case status != http.StatusOK:
		return "", fmt.Errorf("qBittorrent answered %d", status)
	case c.Secret != "" && !apiKeys(body):
		return "", fmt.Errorf("qBittorrent %s has no API keys; upgrade to 5.2 or clear %s", strings.TrimSpace(body), KeyQbitAPIKey)
	}
	return "", nil
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

func testTMDB(ctx context.Context, c integrations.Conn) (string, error) {
	status, _, err := bearerGet(ctx, c, TMDBAPI+"/configuration")
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized:
		return "", fmt.Errorf("%w: TMDB refused the read token", integrations.ErrAuth)
	case status != http.StatusOK:
		return "", fmt.Errorf("TMDB answered %d", status)
	}
	return "", nil
}

func bearerGet(ctx context.Context, c integrations.Conn, target string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, "", err
	}
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), err
}
