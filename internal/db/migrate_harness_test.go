package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/testutil/golden"
)

const harnessDBEnv = "ATRIUM_MIGRATION_HARNESS_DB"

func withFutureMigration(t *testing.T) {
	t.Helper()
	saved := migrations
	next := saved[len(saved)-1].version + 1
	migrations = append(slices.Clone(saved), migration{next, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE harness_future (id INTEGER PRIMARY KEY); ALTER TABLE messages ADD COLUMN harness_future TEXT")
		return err
	}})
	t.Cleanup(func() { migrations = saved })
}

func virtualCounts(t *testing.T, d *DB) map[string]int {
	t.Helper()
	shape, err := readSchema(t.Context(), d.Read)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for name, kind := range shape.objects {
		if kind != "fts5" && kind != "vec0" {
			continue
		}
		var n int
		if err := d.Read.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q", name)).Scan(&n); err != nil {
			t.Fatalf("%s %s: %v", kind, name, err)
		}
		out[kind+" "+name] = n
	}
	return out
}

func harnessRun(t *testing.T, src string) (before, after map[string]int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atrium.db")
	if src != "" {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ctx := t.Context()
	pre, err := readSchema(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	before = virtualCounts(t, d)
	if err := Migrate(ctx, d, path); err != nil {
		t.Fatal(err)
	}
	post, err := readSchema(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	if lost := pre.missingFrom(post); len(lost) > 0 {
		t.Errorf("migrations are not additive, the migrated DB lacks %v", lost)
	}
	after = virtualCounts(t, d)
	for name, n := range before {
		if after[name] != n {
			t.Errorf("%s: %d rows before the migrations, %d after", name, n, after[name])
		}
	}
	if err := Migrate(ctx, d, path); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	again := virtualCounts(t, d)
	if !maps.Equal(after, again) {
		t.Errorf("second Migrate changed the FTS5 and vec0 row counts: %v then %v", after, again)
	}
	var applied int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) {
		t.Errorf("schema_migrations has %d rows, want one per migration (%d)", applied, len(migrations))
	}
	var quick string
	if err := d.Read.QueryRow("PRAGMA quick_check").Scan(&quick); err != nil || quick != "ok" {
		t.Errorf("quick_check: %q, %v", quick, err)
	}
	return before, after
}

func TestMigrationHarnessFreshDB(t *testing.T) {
	withFutureMigration(t)
	_, after := harnessRun(t, "")
	var fts, vec int
	for key, n := range after {
		if n != 0 {
			t.Errorf("%s: %d rows on a fresh DB", key, n)
		}
		if strings.HasPrefix(key, "fts5 ") {
			fts++
		} else {
			vec++
		}
	}
	if fts == 0 || vec == 0 {
		t.Errorf("fresh DB has %d FTS5 and %d vec0 tables, want both", fts, vec)
	}
}

func TestMigrationHarnessGoldenDB(t *testing.T) {
	withFutureMigration(t)
	before, _ := harnessRun(t, filepath.Join(golden.Dir(), "atrium.db"))
	var populated int
	for _, n := range before {
		if n > 0 {
			populated++
		}
	}
	if populated == 0 {
		t.Error("the golden DB has no FTS5 or vec0 rows, so the drift check proves nothing")
	}
}

func TestMigrationHarnessRealDBCopy(t *testing.T) {
	src := os.Getenv(harnessDBEnv)
	if src == "" {
		t.Skipf("%s is not set", harnessDBEnv)
	}
	before, after := harnessRun(t, src)
	for name, n := range before {
		t.Logf("%s: %d rows before, %d after", name, n, after[name])
	}
}
