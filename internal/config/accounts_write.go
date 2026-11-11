package config

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
)

func SaveStoredAccount(ctx context.Context, q db.Querier, a AccountSpec, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO mail_accounts (id, label, preset, login, enabled, auth_method, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET label = excluded.label, login = excluded.login, enabled = excluded.enabled, auth_method = excluded.auth_method",
		a.Name, a.Label, a.Preset, a.Login, a.Enabled, a.AuthMethod, now)
	return err
}

func DeleteStoredAccount(ctx context.Context, q db.Querier, id string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM mail_accounts WHERE id = ?", id)
	return err
}
