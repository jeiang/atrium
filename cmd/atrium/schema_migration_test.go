package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func baselineV1(t *testing.T, path string) {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join("..", "..", "internal", "db", "schema", "*.sql"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("baseline scripts: %v %v", scripts, err)
	}
	slices.Sort(scripts)
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := d.WithTx(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at REAL NOT NULL)"); err != nil {
			return err
		}
		for _, name := range scripts {
			text, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(string(text)); err != nil {
				return err
			}
		}
		_, err := tx.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (1, 1.0)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO threads (kind, title, effort, created_at, updated_at) VALUES ('chat', 'before migration 2', 'medium', 1, 1)"); err != nil {
		t.Fatal(err)
	}
}

func runIn(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	cmd := cliExitProcessArgs(t, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return exitCode(cmd.Run()), stdout.String(), stderr.String()
}

func schemaDump(t *testing.T, path string) []string {
	t.Helper()
	d := mustConnect(t, path)
	defer func() { _ = d.Close() }()
	rows, err := d.Read.Query("SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var typ, name, tbl, text string
		if err := rows.Scan(&typ, &name, &tbl, &text); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join([]string{typ, name, tbl, text}, "\t"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSchemaVersionOneDatabaseMigratesToTheFreshSchema(t *testing.T) {
	old := t.TempDir()
	path := filepath.Join(old, "atrium.db")
	baselineV1(t, path)

	if code, _, stderr := runIn(t, old, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list on a version 1 database: exit %d %s", code, stderr)
	}
	d := mustConnect(t, path)
	var title string
	var tainted sql.NullFloat64
	if err := d.Read.QueryRow("SELECT title, tainted_at FROM threads WHERE title = 'before migration 2'").Scan(&title, &tainted); err != nil {
		t.Fatalf("thread after migration: %v", err)
	}
	if tainted.Valid {
		t.Errorf("migrated thread tainted_at = %v, want NULL", tainted.Float64)
	}
	var version int
	if err := d.Read.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != db.LatestSchemaVersion() || version < 2 {
		t.Errorf("schema version = %d, %v; want %d", version, err, db.LatestSchemaVersion())
	}
	_ = d.Close()
	if code, stdout, stderr := runIn(t, old, "db", "check"); code != cli.ExitOK || !strings.Contains(stdout, "integrity_check: ok") {
		t.Fatalf("db check: exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	fresh := t.TempDir()
	if code, _, stderr := runIn(t, fresh, "settings", "list"); code != cli.ExitOK {
		t.Fatalf("settings list on a fresh database: exit %d %s", code, stderr)
	}
	migrated, created := schemaDump(t, path), schemaDump(t, filepath.Join(fresh, "atrium.db"))
	if !slices.Equal(migrated, created) {
		for _, line := range migrated {
			if !slices.Contains(created, line) {
				t.Errorf("only in the migrated schema: %s", line)
			}
		}
		for _, line := range created {
			if !slices.Contains(migrated, line) {
				t.Errorf("only in the fresh schema: %s", line)
			}
		}
	}
}
