package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ActionLinkKey     = "action_link_key"
	ActionLinkKeyFile = "action_link_key_file"
	LinkKeyFile       = "link_key_file"
)

func init() {
	settings.MustRegisterHost(HostSettings()...)
	settings.MustRegister(Settings()...)
}

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: ActionLinkKeyFile, Module: "app_keys", Type: settings.String},
		{Key: LinkKeyFile, Module: "app_keys", Type: settings.String},
	}
}

func Settings() []settings.Def {
	return []settings.Def{
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: ActionLinkKey, Module: "action_links", Type: settings.String, Check: settings.NonEmpty}),
	}
}

func FileKey(ctx context.Context, key string) ([]byte, bool, error) {
	path := strings.TrimSpace(config.HostValueFrom(ctx, key))
	if path == "" {
		return nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("app key %s: %w", key, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return nil, false, fmt.Errorf("app key %s: %s is empty", key, path)
	}
	return []byte(value), true, nil
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
