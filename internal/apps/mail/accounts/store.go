package accounts

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func SaveStoredAccount(ctx context.Context, q db.Querier, a config.AccountSpec, now float64) error {
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

func SavePinnedAccount(ctx context.Context, q db.Querier, a config.AccountSpec, connection string, now float64) error {
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

func LoadStored(ctx context.Context, q db.Querier, filePasswords map[string]string, credential config.AccountCredentialFunc) ([]config.AccountSpec, map[string]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, label, preset, login, enabled, pinned, auth_method, connection_id, capability_prefix FROM mail_accounts ORDER BY created_at, id")
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, label, preset, login string
		enabled, pinned          bool
		method                   string
		connection, prefix       sql.NullString
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.label, &x.preset, &x.login, &x.enabled, &x.pinned, &x.method, &x.connection, &x.prefix)
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	accounts := make([]config.AccountSpec, 0, len(stored))
	passwords := make(map[string]string, len(stored))
	for _, x := range stored {
		connection := x.connection.String
		if connection == "" {
			connection = "mail/" + x.id
		}
		prefix := x.prefix.String
		if prefix == "" {
			prefix = "mail/" + x.id
		}
		password := filePasswords[x.id]
		if credential != nil {
			c, err := credential(ctx, q, connection, prefix+"/password")
			if err != nil {
				return nil, nil, fmt.Errorf("config: mail account %q credential: %w", x.id, err)
			}
			switch c.Source {
			case "database":
				if c.Value != "" {
					password = c.Value
				}
			case "host_file":
				raw, err := os.ReadFile(c.FileRef)
				if err != nil {
					return nil, nil, fmt.Errorf("config: mail account %q credential: %w", x.id, err)
				}
				if value := config.NormalizePassword(string(raw)); value != "" {
					password = value
				}
			}
		}
		if password != "" {
			passwords[x.id] = password
		}
		a, err := NewAccount(x.id, x.label, x.preset, x.login, x.enabled)
		if err != nil {
			return nil, nil, fmt.Errorf("config: stored mail account %q: %w", x.id, err)
		}
		if a.AuthMethod, err = config.ParseAuthMethod(x.method); err != nil {
			return nil, nil, fmt.Errorf("config: stored mail account %q: %w", x.id, err)
		}
		a.Pinned = x.pinned
		a.ConnectionID = connection
		a.CapabilityPrefix = prefix
		accounts = append(accounts, a)
	}
	return accounts, passwords, nil
}
