package secrets

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ActionLinkKey    = "action_link_key"
	OIDCClientSecret = "oidc_client_secret"

	GoogleOAuthClientSecret    = "google_oauth_client_secret"
	GoogleCalendarRefreshToken = "google_calendar_refresh_token"
	GoogleGmailRefreshToken    = "google_gmail_refresh_token"
)

func init() {
	settings.MustRegister(
		settings.Secret(settings.SecretOpts{Encrypted: true, Readable: true}, settings.Def{Key: OIDCClientSecret, Module: "auth", Type: settings.String}),
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: ActionLinkKey, Module: "action_links", Type: settings.String, Check: settings.NonEmpty}),
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: GoogleOAuthClientSecret, Module: "calendar", Type: settings.String, EnvAlias: "GOOGLE_OAUTH_CLIENT_SECRET"}),
		settings.Secret(settings.SecretOpts{Encrypted: true, Readable: true}, settings.Def{Key: GoogleCalendarRefreshToken, Module: "calendar", Type: settings.String, EnvAlias: "GOOGLE_REFRESH_TOKEN"}),
		settings.Secret(settings.SecretOpts{Encrypted: true, Readable: true}, settings.Def{Key: GoogleGmailRefreshToken, Module: "mail", Type: settings.String, EnvAlias: "GOOGLE_GMAIL_REFRESH_TOKEN"}),
	)
}

func New(reg *settings.Registry, d *db.DB, cipher settings.Cipher) *settings.Store {
	return settings.NewStore(reg, d, cipher)
}

func NewIn(reg *settings.Registry, d *db.DB, q db.Querier, cipher settings.Cipher) *settings.Store {
	return settings.NewStoreIn(reg, d, q, cipher)
}

func Credential(ctx context.Context, q db.Querier, name string) (string, error) {
	return settings.NewStoreIn(settings.From(ctx), nil, q, nil).Credential(ctx, name)
}
