package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

//go:embed schema
var schemaFS embed.FS

func initSchema(ctx context.Context, q Querier) error {
	if _, err := q.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", ApplicationID)); err != nil {
		return fmt.Errorf("db: application id: %w", err)
	}
	scripts, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(scripts)
	for _, name := range scripts {
		text, err := schemaFS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, string(text)); err != nil {
			return fmt.Errorf("db: schema %s: %w", name, err)
		}
	}
	return nil
}
