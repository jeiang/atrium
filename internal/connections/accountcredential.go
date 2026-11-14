package connections

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func AccountCredential() config.AccountCredentialFunc { return credentialAt }

func credentialAt(ctx context.Context, q db.Querier, id, slot string) (config.AccountCredential, error) {
	var (
		c       config.AccountCredential
		value   sql.NullString
		fileRef sql.NullString
	)
	err := q.QueryRowContext(ctx,
		"SELECT source, value, file_ref FROM connection_credentials WHERE connection_id = ? AND slot = ?", ID(id), slot).
		Scan(&c.Source, &value, &fileRef)
	if errors.Is(err, sql.ErrNoRows) {
		return config.AccountCredential{}, nil
	}
	if err != nil {
		return config.AccountCredential{}, err
	}
	c.Value, c.FileRef = value.String, fileRef.String
	if c.Source == "host_file" {
		c.FileRef = config.From(ctx).Host().Connections[id].CredentialFiles[slot]
		if c.FileRef == "" {
			return config.AccountCredential{}, MissingCredential{Slot: slot}
		}
	}
	return c, nil
}
