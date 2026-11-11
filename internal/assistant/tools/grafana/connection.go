package grafanatools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
)

const KindGrafana connections.Kind = "service/grafana"

type connectionConfig struct {
	URL        string `json:"url"`
	MetricsUID string `json:"metrics_uid"`
	LogsUID    string `json:"logs_uid"`
	LogsType   string `json:"logs_type"`
	LogsQuery  string `json:"logs_query"`
}

func init() {
	connections.Register(connections.Definition{
		Kind:     KindGrafana,
		Validate: validateConnection,
		Test:     testConnection,
	})
}

func validateConnection(c connections.Candidate) error {
	var cfg connectionConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("grafana config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("grafana needs a URL")
	}
	if err := httpURL(cfg.URL); err != nil {
		return err
	}
	if cfg.LogsType != "" && cfg.LogsType != Loki && cfg.LogsType != VictoriaLogs {
		return fmt.Errorf("grafana logs type must be %s or %s", Loki, VictoriaLogs)
	}
	return nil
}

func httpURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http or https URL")
	}
	return nil
}

func testConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	client, err := lease.HTTP("url")
	if err != nil {
		return nil, err
	}
	token, err := grafanaToken(lease)
	if err != nil {
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: "the connection has no viewer token"}}, nil
	}
	status, body, err := viewerGet(ctx, client, base, token, "/api/user")
	if err != nil {
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: err.Error()}}, nil
	}
	switch status {
	case http.StatusUnauthorized:
		return []connections.Result{{Capability: "viewer", State: connections.NeedsReauth, Reason: "Grafana refused the token"}}, nil
	case http.StatusOK:
	default:
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: fmt.Sprintf("Grafana answered %d", status)}}, nil
	}
	var user struct{ Login string }
	if err := json.Unmarshal([]byte(body), &user); err != nil {
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: "Grafana returned an unreadable user profile"}}, nil
	}
	if user.Login == "" {
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: "Grafana returned a user profile without a login"}}, nil
	}
	status, _, err = viewerGet(ctx, client, base, token, "/api/search?limit=1")
	if err != nil {
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: err.Error()}}, nil
	}
	switch {
	case status == http.StatusForbidden:
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: user.Login + " may not search dashboards; give it the Viewer role"}}, nil
	case status != http.StatusOK:
		return []connections.Result{{Capability: "viewer", State: connections.Failing, Reason: fmt.Sprintf("Grafana answered %d searching dashboards", status)}}, nil
	}
	data, err := json.Marshal(map[string]string{"login": user.Login})
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "viewer", State: connections.OK, Reason: "signed in as " + user.Login, Data: data}}, nil
}

func grafanaToken(lease *connections.Lease) (string, error) {
	if token, err := lease.Credential("viewer_token"); err == nil && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token), nil
	}
	token, err := lease.Credential("api_key")
	return strings.TrimSpace(token), err
}

func viewerGet(ctx context.Context, client *http.Client, base, token, path string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), err
}
