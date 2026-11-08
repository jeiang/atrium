package contacts

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	DirKey = "contacts_dir"
	DirEnv = "ATRIUM_CONTACTS_" + DirKey
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: DirKey, Module: "contacts", Type: settings.String}))
}

func Dir(ctx context.Context, q db.Querier) (string, error) {
	on, err := settings.Default.Resolve(ctx, q, module.FlagKey(module.Contacts))
	if err != nil || on.Value != "true" {
		return "", err
	}
	found, err := settings.Default.Resolve(ctx, q, DirKey)
	if err != nil || found.Value == "" {
		return "", err
	}
	return filepath.Clean(found.Value), nil
}
