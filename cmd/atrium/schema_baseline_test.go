//go:build !modernc

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const schemaBaselineQuery = "SELECT json_array(type,name,tbl_name,sql) FROM sqlite_master ORDER BY type,name,tbl_name;\nPRAGMA application_id;\nPRAGMA user_version;\n"

func schemaBaselineEnv(dir, path, key string) []string {
	return []string{"ATRIUM_DB=" + path, "ATRIUM_DB_KEY=" + key, "ATRIUM_CACHE_DIR=" + filepath.Join(dir, "cache"), "ATRIUM_CONFIG=", "ATRIUM_HOST_CONFIG=", "CREDENTIALS_DIRECTORY="}
}

func schemaBaselineDump(t *testing.T, env []string) string {
	t.Helper()
	got := runProcessStdin(t, schemaBaselineQuery, "db shell", env...)
	if got.code != cli.ExitOK {
		t.Fatalf("reading schema through db shell: %s", got.stderr)
	}
	lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
	if len(lines) < 2 {
		t.Fatalf("incomplete schema output: %q", got.stdout)
	}
	for i := range lines[:len(lines)-2] {
		var row []any
		if err := json.Unmarshal([]byte(lines[i]), &row); err != nil {
			t.Fatal(err)
		}
		if len(row) != 4 {
			t.Fatalf("invalid sqlite_master row: %q", lines[i])
		}
		if sql, ok := row[3].(string); ok {
			row[3] = strings.Join(strings.Fields(sql), " ")
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(raw)
	}
	lines[len(lines)-2] = "application_id=" + lines[len(lines)-2]
	lines[len(lines)-1] = "user_version=" + lines[len(lines)-1]
	return strings.Join(lines, "\n") + "\n"
}

func migrateSchemaFixture(t *testing.T, fixture, want string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "migrated.db")
	copyFile(t, fixture, path)
	env := schemaBaselineEnv(dir, path, "")
	if got := runProcess(t, "settings list", env...); got.code != cli.ExitOK {
		t.Fatalf("migrating previous schema through CLI: %s", got.stderr)
	}
	if got := schemaBaselineDump(t, env); got != want {
		t.Fatalf("the v3 baseline is frozen: add a migration; migrated fixture %s differs from a fresh database\n%s", filepath.Base(fixture), got)
	}
}

func assertPreviousMigrationSchemas(t *testing.T, env []string, fresh string) {
	t.Helper()
	got := runProcessStdin(t, "SELECT version FROM schema_migrations WHERE version > 1 ORDER BY version;\n", "db shell", env...)
	if got.code != cli.ExitOK {
		t.Fatalf("reading migration versions: %s", got.stderr)
	}
	for _, line := range strings.Fields(got.stdout) {
		version, err := strconv.Atoi(line)
		if err != nil {
			t.Fatal(err)
		}
		fixture := filepath.Join(golden.Dir(), fmt.Sprintf("schema-v3-%d.db", version-1))
		if version == 2 {
			fixture = filepath.Join(golden.Dir(), "world.db")
		}
		t.Run(fmt.Sprintf("migration-%d", version), func(t *testing.T) {
			migrateSchemaFixture(t, fixture, fresh)
		})
	}
}

func TestSchemaBaselineFrozenAndPreviousMigrationsMatchFresh(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "baseline.db")
	copyFile(t, filepath.Join(golden.Dir(), "world.db"), fixture)
	env := schemaBaselineEnv(dir, fixture, "")
	versions := runProcessStdin(t, "SELECT version FROM schema_migrations ORDER BY version;\n", "db shell", env...)
	if versions.code != cli.ExitOK || versions.stdout != "1\n" {
		t.Fatalf("frozen world.db must remain at migration 1: exit %d, versions %q, error %s", versions.code, versions.stdout, versions.stderr)
	}
	baseline, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "schema", "baseline.v3.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaBaselineDump(t, env); got != string(baseline) {
		t.Fatalf("the v3 baseline is frozen: add a migration; world.db differs from the committed baseline\n%s", got)
	}
	freshEnv := schemaBaselineEnv(dir, filepath.Join(dir, "fresh.db"), rightKeyHex)
	if got := runProcess(t, "settings list", freshEnv...); got.code != cli.ExitOK {
		t.Fatalf("creating keyed fresh database through CLI: %s", got.stderr)
	}
	if isPlaintextSQLite(t, filepath.Join(dir, "fresh.db")) {
		t.Fatal("fresh keyed database has a plaintext header")
	}
	fresh := schemaBaselineDump(t, freshEnv)
	migrateSchemaFixture(t, fixture, fresh)
	assertPreviousMigrationSchemas(t, freshEnv, fresh)
}
