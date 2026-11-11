package config

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/db"
)

func SaveStoredAccount(ctx context.Context, q db.Querier, a AccountSpec, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO mail_accounts (id, label, preset, login, enabled, auth_method, connection_id, capability_prefix, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET label = excluded.label, login = excluded.login, enabled = excluded.enabled, auth_method = excluded.auth_method, "+
			"connection_id = excluded.connection_id, capability_prefix = excluded.capability_prefix",
		a.Name, a.Label, a.Preset, a.Login, a.Enabled, a.AuthMethod, "mail/"+a.Name, "mail/"+a.Name, now)
	return err
}

func DeleteStoredAccount(ctx context.Context, q db.Querier, id string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM mail_accounts WHERE id = ?", id)
	return err
}

// SavePinnedAccount stores a host-declared account and pins it. An empty
// connection leaves an existing link untouched.
func SavePinnedAccount(ctx context.Context, q db.Querier, a AccountSpec, connection string, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, connection_id, capability_prefix, created_at) "+
			"VALUES (?, ?, ?, ?, ?, 1, ?, NULLIF(?, ''), ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET label = excluded.label, preset = excluded.preset, login = excluded.login, enabled = excluded.enabled, "+
			"auth_method = excluded.auth_method, pinned = 1, connection_id = COALESCE(excluded.connection_id, mail_accounts.connection_id), "+
			"capability_prefix = excluded.capability_prefix",
		a.Name, a.Label, a.Preset, a.Login, a.Enabled, a.AuthMethod, connection, "mail/"+a.Name, now)
	return err
}

func PinnedAccounts(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT id FROM mail_accounts WHERE pinned = 1")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var id string
		return id, r.Scan(&id)
	})
}
