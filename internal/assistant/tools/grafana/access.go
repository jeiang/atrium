package grafanatools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
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

func init() {
	settings.MustRegister(settings.Deploy(settings.Def{Key: KeyURL, Module: "grafana", Type: settings.String}))
}

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
