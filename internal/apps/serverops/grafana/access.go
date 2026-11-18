package grafanatools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

type Access struct {
	URL        string
	Token      string
	HTTP       *http.Client
	MetricsUID string
	LogsUID    string
	LogsType   string
	LogsQuery  string
}

const (
	KeyURL   = "grafana_url"
	KeyToken = "grafana_viewer_token"
)

func Load(ctx context.Context, q db.Querier) (Access, error) {
	lease, err := connections.OpenRead(ctx, q, config.From(ctx).Host(), connections.ID(KindGrafana))
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return Access{}, config.NotConfigured("the Grafana connection is not configured")
		}
		return Access{}, err
	}
	base, err := lease.Endpoint("url")
	if err != nil {
		return Access{}, config.NotConfigured("the Grafana connection has no URL")
	}
	token, err := grafanaToken(lease)
	if err != nil {
		return Access{}, config.NotConfigured("the Grafana connection has no viewer token")
	}
	client, err := lease.HTTP("url")
	if err != nil {
		return Access{}, err
	}
	var cfg connectionConfig
	if err := json.Unmarshal(lease.Config(), &cfg); err != nil {
		return Access{}, err
	}
	return Access{
		URL: strings.TrimRight(strings.TrimSpace(base), "/"), Token: token, HTTP: client,
		MetricsUID: cfg.MetricsUID, LogsUID: cfg.LogsUID, LogsType: cfg.LogsType, LogsQuery: cfg.LogsQuery,
	}, nil
}

func Link(ctx context.Context, q db.Querier) (string, error) {
	lease, err := connections.OpenRead(ctx, q, config.From(ctx).Host(), connections.ID(KindGrafana))
	if errors.As(err, new(connections.UnknownID)) || errors.As(err, new(connections.DisabledConnection)) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	base, err := lease.Endpoint("url")
	if err != nil {
		return "", nil
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", nil
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimRight(u.String(), "/"), nil
}
