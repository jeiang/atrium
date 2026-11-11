package media

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/jeiang/atrium/internal/integrations"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "tmdb", Label: "TMDB", Module: module, URLKey: KeyTMDBURL, SecretKey: KeyTMDBToken,
		Help: "API read access token from your TMDB account settings.",
		Test: testTMDB,
	})
	integrations.RegisterSection(integrations.Section{
		Name: "media", Title: "Media libraries", Module: module,
		Fields: []integrations.Field{
			{Key: KeyMoviesURL, Label: "Movies URL"},
			{Key: KeyTVURL, Label: "TV URL"},
		},
	})
}

func testTMDB(ctx context.Context, c integrations.Conn) (string, error) {
	status, _, err := bearerGet(ctx, c, c.URL+"/configuration")
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
