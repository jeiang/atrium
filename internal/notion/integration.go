package notion

import (
	"context"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
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
	settings.MustRegister(settings.Def{Key: TokenKey, Module: "notion", Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true})
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
	if _, err := client.Request(ctx, http.MethodGet, "/v1/users/me", nil, nil); err != nil {
		return 0, false, err
	}
	filter := py.Dict{{Key: "property", Value: "object"}, {Key: "value", Value: "page"}}
	reply, err := client.Request(ctx, http.MethodPost, "/v1/search", py.Dict{{Key: "filter", Value: filter}, {Key: pagedKeyPerPage, Value: 100}}, nil)
	if err != nil {
		return 0, false, err
	}
	return len(list(reply, "results")), py.Truthy(field(reply, "has_more")), nil
}
