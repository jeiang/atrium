package secrets

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AnthropicAPIKey   = "anthropic_api_key"
	AnthropicAdminKey = "anthropic_admin_key"
	OpenAIAdminKey    = "openai_admin_key"
	ActionLinkKey     = "action_link_key"
	OIDCClientSecret  = "oidc_client_secret"
)

func init() {
	settings.MustRegister(
		settings.Secret(settings.SecretOpts{Encrypted: true, Readable: true}, settings.Def{Key: AnthropicAPIKey, Module: "anthropic", Type: settings.String, EnvAlias: "ANTHROPIC_API_KEY"}),
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: AnthropicAdminKey, Module: "anthropic", Type: settings.String}),
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: OpenAIAdminKey, Module: "openai", Type: settings.String}),
		settings.Secret(settings.SecretOpts{Encrypted: true, Readable: true}, settings.Def{Key: OIDCClientSecret, Module: "auth", Type: settings.String}),
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: ActionLinkKey, Module: "action_links", Type: settings.String, Check: settings.NonEmpty}),
	)
}

func init() {
	db.OnReady(func(ctx context.Context, d *db.DB) error {
		return settings.NewStore(settings.Default, d, d).Load(ctx)
	})
}

func New(d *db.DB, cipher settings.Cipher) *settings.Store {
	return settings.NewStore(settings.Default, d, cipher)
}

func NewIn(d *db.DB, q db.Querier, cipher settings.Cipher) *settings.Store {
	return settings.NewStoreIn(settings.Default, d, q, cipher)
}

func Credential(ctx context.Context, q db.Querier, name string) (string, error) {
	return settings.NewStoreIn(settings.Default, nil, q, nil).Credential(ctx, name)
}
