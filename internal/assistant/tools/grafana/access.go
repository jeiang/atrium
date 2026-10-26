package grafanatools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyURL   = "grafana_url"
	KeyToken = "grafana_viewer_token"
)

func init() {
	settings.MustRegister(
		settings.Def{Key: KeyURL, Module: "grafana", Type: settings.String, Class: settings.UI, Scopes: []settings.Scope{settings.Instance}},
		settings.Def{Key: KeyToken, Module: "grafana", Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true},
	)
	integrations.Register(integrations.Integration{
		Name: "grafana", Label: "Grafana", Module: "grafana", URLKey: KeyURL, SecretKey: KeyToken,
		Help: "Service account token with the Viewer role.",
		Test: testAccess,
	})
}

type Access struct {
	URL   string
	Token string
}

func Load(ctx context.Context, q db.Querier) (Access, error) {
	found, err := settings.Default.Resolve(ctx, q, KeyURL, 0)
	if err != nil {
		return Access{}, err
	}
	token, err := secrets.NewIn(nil, q, nil).Value(ctx, KeyToken)
	if err != nil {
		return Access{}, err
	}
	a := Access{URL: strings.TrimRight(strings.TrimSpace(found.Value), "/"), Token: strings.TrimSpace(token)}
	if a.URL == "" || a.Token == "" {
		return Access{}, config.NotConfigured(KeyURL + " and " + KeyToken + " must be set")
	}
	return a, nil
}

func testAccess(ctx context.Context, c integrations.Conn) (string, error) {
	if c.Secret == "" {
		return "", fmt.Errorf("%w: no service account token", integrations.ErrAuth)
	}
	status, body, err := viewerGet(ctx, c, "/api/user")
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		return "", fmt.Errorf("%w: Grafana refused the token", integrations.ErrAuth)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("grafana answered %d", status)
	}
	var user struct{ Login string }
	_ = json.Unmarshal([]byte(body), &user)
	status, _, err = viewerGet(ctx, c, "/api/search?limit=1")
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusForbidden:
		return "", fmt.Errorf("%s may not search dashboards; give it the Viewer role", user.Login)
	case status != http.StatusOK:
		return "", fmt.Errorf("grafana answered %d searching dashboards", status)
	}
	return "signed in as " + user.Login, nil
}

func viewerGet(ctx context.Context, c integrations.Conn, path string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), err
}
