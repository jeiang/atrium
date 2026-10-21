package contacts

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const DirKey = "contacts_dir"

func init() {
	settings.MustRegister(settings.Def{
		Key: DirKey, Module: "contacts", Type: settings.String, Class: settings.Advanced,
		Scopes: []settings.Scope{settings.Instance},
	})
}

func Dir(ctx context.Context, q db.Querier) (string, error) {
	found, err := settings.Default.Resolve(ctx, q, DirKey, 0)
	if err != nil || found.Value == "" {
		return "", err
	}
	return filepath.Clean(found.Value), nil
}
