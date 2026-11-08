package notion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const TokenKey = "notion_token"

var apiBase = BaseURL

func UseBaseURL(u string) func() {
	saved := apiBase
	apiBase = u
	return func() { apiBase = saved }
}

func init() {
	settings.MustRegister(settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: TokenKey, Module: "notion", Type: settings.String}))
}

func Token(ctx context.Context, q db.Querier) (string, error) {
	token, err := secrets.NewIn(nil, q, nil).Value(ctx, TokenKey)
	if err != nil {
		return "", err
	}
	if token = strings.TrimSpace(token); token == "" {
		return "", config.NotConfigured(TokenKey + " is not set")
	}
	return token, nil
}

func SharedPages(ctx context.Context, token string, hc *http.Client) (shared int, more bool, err error) {
	client := NewClient(token)
	client.HTTP = hc
	client.API.MaxRetries = 0
	if err := client.do(ctx, http.MethodGet, "/v1/users/me", nil, nil); err != nil {
		return 0, false, err
	}
	var reply struct {
		Results []json.RawMessage `json:"results"`
		HasMore bool              `json:"has_more"`
	}
	body := map[string]any{"filter": map[string]any{"property": "object", "value": "page"}, pagedKeyPerPage: 100}
	if err := client.do(ctx, http.MethodPost, "/v1/search", body, &reply); err != nil {
		return 0, false, err
	}
	return len(reply.Results), reply.HasMore, nil
}
