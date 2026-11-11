package secrets

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ActionLinkKey = "action_link_key"
	OidcWizardKey = "oidc_wizard_key"
)

func init() {
	settings.MustRegister(
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: ActionLinkKey, Module: "action_links", Type: settings.String, Check: settings.NonEmpty}),
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: OidcWizardKey, Module: "auth", Type: settings.String, Check: settings.NonEmpty}),
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
