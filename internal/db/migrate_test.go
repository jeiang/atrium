package db_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const migrationsLine = "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at REAL NOT NULL);\n"

func schemaSQL(t *testing.T, d *db.DB) string {
	t.Helper()
	rows, err := d.Read.Query("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'view' THEN 2 ELSE 3 END, name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s + ";\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func withoutMigrations(t *testing.T, schema string) string {
	t.Helper()
	if !strings.Contains(schema, migrationsLine) {
		t.Fatalf("schema has no schema_migrations table")
	}
	return strings.Replace(schema, migrationsLine, "", 1)
}

func diffLines(t *testing.T, label, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			t.Errorf("%s line %d:\nwant %q\ngot  %q", label, i+1, a, b)
			return
		}
	}
}

func rowCounts(t *testing.T, d *db.DB) map[string]int {
	t.Helper()
	rows, err := d.Read.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, name := range names {
		r, err := d.Read.Query(fmt.Sprintf("SELECT * FROM %q", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cols, err := r.Columns()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		n := 0
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			n++
		}
		if err := r.Err(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = r.Close()
		out[name] = n
	}
	return out
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(golden.Dir(), path))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(golden.Dir(), path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func checkCounts(t *testing.T, d *db.DB, path string) {
	t.Helper()
	var want map[string]int
	readJSON(t, path, &want)
	got := rowCounts(t, d)
	if got["schema_migrations"] != 1 {
		t.Errorf("schema_migrations has %d rows, want 1", got["schema_migrations"])
	}
	delete(got, "schema_migrations")
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s: %d rows, golden %d", name, got[name], n)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("table %s is not in %s", name, path)
		}
	}
}

func quickCheck(t *testing.T, d *db.DB) {
	t.Helper()
	var ok string
	if err := d.Read.QueryRow("PRAGMA quick_check").Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok != "ok" {
		t.Errorf("quick_check: %s", ok)
	}
}

func TestFreshSchema(t *testing.T) {
	d := dbtest.Schema(t)
	diffLines(t, "schema.sql", readText(t, "db/schema.sql"), withoutMigrations(t, schemaSQL(t, d)))
	var n int
	for table, want := range map[string]int{"skills": 4, "subagents": 5, "categories": 17} {
		if err := d.Read.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: %d seeded rows, want %d", table, n, want)
		}
	}
}

func TestGoldenDB(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	diffLines(t, "schema.sql", readText(t, "db/schema.sql"), withoutMigrations(t, schemaSQL(t, d)))
	checkCounts(t, d, "db/rowcounts.json")
	quickCheck(t, d)
}

func TestLegacyDB(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium-legacy.db"))
	diffLines(t, "legacy/migrated/schema.sql", readText(t, "db/legacy/migrated/schema.sql"), withoutMigrations(t, schemaSQL(t, d)))
	checkCounts(t, d, "db/legacy/migrated/rowcounts.json")
	quickCheck(t, d)
	var forex int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM money_accounts WHERE forex = 1").Scan(&forex); err != nil {
		t.Fatal(err)
	}
	if forex == 0 {
		t.Error("forex backfill set no account")
	}
}

func TestSeedsReappear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ctx := t.Context()
	if err := db.Migrate(ctx, d, path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM skills WHERE name = 'wiki-query'; DELETE FROM categories WHERE name = 'Other'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, path); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM skills WHERE name = 'wiki-query') + (SELECT COUNT(*) FROM categories WHERE name = 'Other')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("deleted seeds back: %d of 2", n)
	}
}

func migrateAsync(path string) chan error {
	done := make(chan error, 1)
	go func() {
		d, err := db.Connect(path)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = d.Close() }()
		done <- db.Migrate(context.Background(), d, path)
	}()
	return done
}

func TestNoFlockWhenNothingPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	first := migrateAsync(path)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	holder := child(t, "locker", path)
	if _, err := holder.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "locked" {
		t.Fatalf("locker printed %q", line)
	}
	select {
	case err := <-migrateAsync(path):
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Migrate blocked on atrium.db.lock with no migration pending")
	}
}

func TestPendingWaitsForFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	lock, err := db.Exclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	done := migrateAsync(path)
	select {
	case err := <-done:
		t.Fatalf("Migrate finished (%v) while another holder had atrium.db.lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Migrate did not finish after the lock was released")
	}
}

func migratorProcess(path string) int {
	d, err := db.Connect(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(context.Background(), d, path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestConcurrentStartProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	cmds := make([]*exec.Cmd, 4)
	for i := range cmds {
		cmds[i] = child(t, "migrator", path)
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("migrator %d: %v", i, err)
		}
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_migrations: %d rows, want 1", n)
	}
	diffLines(t, "schema.sql", readText(t, "db/schema.sql"), withoutMigrations(t, schemaSQL(t, d)))
}
