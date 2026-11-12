package anthropicconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
)

const Kind connections.Kind = "service/anthropic"

type connectionConfig struct {
	URL string `json:"url"`
}

func init() {
	for _, d := range Connections() {
		connections.Register(d)
	}
}

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     Kind,
		Validate: validate,
		Test:     test,
	}}
}

func validate(c connections.Candidate) error {
	var cfg connectionConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return fmt.Errorf("anthropic config: %w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("anthropic needs a URL")
	}
	return httpURL(cfg.URL)
}

func test(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	key, err := lease.Credential("api_key")
	if err != nil {
		return []connections.Result{{Capability: "models", State: connections.Failing, Reason: "the connection has no API key"}}, nil
	}
	client, err := lease.HTTP("url")
	if err != nil {
		return nil, err
	}
	c, err := anthropic.New(strings.TrimSpace(key), Env(nil, lease), anthropic.HTTPClient(client))
	if err != nil {
		return nil, err
	}
	ids, err := c.ModelIDs(ctx)
	var failure *anthropic.APIError
	switch {
	case errors.As(err, &failure) && failure.StatusCode == http.StatusUnauthorized:
		return []connections.Result{{Capability: "models", State: connections.NeedsReauth, Reason: "Anthropic refused the key"}}, nil
	case errors.As(err, &failure) && failure.StatusCode == http.StatusForbidden:
		return []connections.Result{{Capability: "models", State: connections.Failing, Reason: "the key may not list models: " + failure.Message}}, nil
	case err != nil:
		return []connections.Result{{Capability: "models", State: connections.Failing, Reason: err.Error()}}, nil
	}
	data, err := json.Marshal(map[string]int{"models": len(ids)})
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "models", State: connections.OK, Reason: "models available", Data: data}}, nil
}

func Env(env config.Env, lease *connections.Lease) config.Env {
	return func(name string) (string, bool) {
		if name == anthropic.BaseURLEnv {
			if env != nil {
				if value, ok := env(name); ok && value != "" {
					return value, true
				}
			}
			if value, err := lease.Endpoint("url"); err == nil {
				return value, true
			}
			return "", false
		}
		if env == nil {
			return "", false
		}
		return env(name)
	}
}

func httpURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http or https URL")
	}
	return nil
}
