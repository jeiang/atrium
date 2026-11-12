package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
)

const KindNotion connections.Kind = "service/notion"

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     KindNotion,
		Validate: validateConnection,
		Test:     testConnection,
	}}
}

func validateConnection(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("notion config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "api":
			if err := httpURL(value); err != nil {
				return fmt.Errorf("notion api %w", err)
			}
		default:
			return fmt.Errorf("notion config has no %s field", key)
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
	token, err := lease.Credential("token")
	if err != nil {
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: "no Notion token configured"}}, nil
	}
	base, err := lease.Endpoint("api")
	if err != nil {
		return nil, err
	}
	hc, err := lease.HTTP("api")
	if err != nil {
		return nil, err
	}
	client := &Client{token: token, BaseURL: strings.TrimRight(base, "/"), HTTP: hc, API: DefaultAPILimits()}
	client.API.MaxRetries = 0
	shared, more, err := client.sharedPages(ctx)
	var refused *Error
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusUnauthorized:
		return []connections.Result{{Capability: "api", State: connections.NeedsReauth, Reason: "Notion refused the token"}}, nil
	case err != nil:
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: err.Error()}}, nil
	case shared == 0:
		return []connections.Result{{Capability: "api", State: connections.Failing, Reason: "no pages shared with the integration"}}, nil
	}
	reason := fmt.Sprintf("%d pages shared", shared)
	if more {
		reason = fmt.Sprintf("%d+ pages shared", shared)
	}
	data, err := json.Marshal(map[string]any{"shared": shared, "more": more})
	if err != nil {
		return nil, err
	}
	return []connections.Result{{Capability: "api", State: connections.OK, Reason: reason, Data: data}}, nil
}
