package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/connections"
)

const (
	KindShip24   connections.Kind = "service/ship24"
	ConnectionID                  = connections.ID("service/ship24")
)

func init() {
	connections.Register(connections.Definition{
		Kind:     KindShip24,
		Validate: validateConnection,
		Test:     testConnection,
	})
}

func validateConnection(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("ship24 config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "url":
			if err := httpURL(value); err != nil {
				return fmt.Errorf("ship24 url %w", err)
			}
		default:
			return fmt.Errorf("ship24 config has no %s field", key)
		}
	}
	return nil
}

func httpURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http or https URL")
	}
	return nil
}

func testConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	base, err := lease.Endpoint("url")
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Unconfigured, Reason: "no Ship24 URL configured"}}, nil
	}
	key, err := lease.Credential("api_key")
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: "no Ship24 API key configured"}}, nil
	}
	hc, err := lease.ClientRead("url", time.Duration(TuningMailShip24TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	status, _, err := NewClient(hc, base, key).do(ctx, http.MethodGet, "/trackers?limit=1", nil)
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: "Ship24 refused the API key"}}, nil
	case http.StatusOK:
		return []connections.Result{{Capability: "api", State: connections.OK, Reason: "API key works"}}, nil
	}
	return []connections.Result{{Capability: "api", State: connections.Failing, Reason: fmt.Sprintf("Ship24 answered %d", status)}}, nil
}
