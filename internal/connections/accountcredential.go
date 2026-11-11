package connections

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func init() { config.RegisterAccountCredential(credentialAt) }

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
	return c, nil
}
