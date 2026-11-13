//go:build !modernc

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestImportV2DropsAndReportsTheOpsCondenserSettings(t *testing.T) {
	t.Parallel()
	clock := float64(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir := t.TempDir()
	source := copyV2Fixture(t, dir)
	src, err := db.Connect(source, db.WithKey(db.Key{}))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"ops_condenser", "ops_condenser_max_tokens", "ops_condenser_model"}
	for i, key := range keys {
		if _, err := src.Write.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, []string{"local", "900", "qwen"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	got := runImportApp(t, importTestApp(t, dir, clock), "db", "import-v2", source)
	if got.code != cli.ExitOK {
		t.Fatalf("import with ops_condenser settings: exit %d stderr %q", got.code, got.stderr)
	}
	dest, err := db.OpenSource(filepath.Join(dir, "atrium.db"), importKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dest.Close() }()
	for _, key := range keys {
		if !strings.Contains(got.stdout, "setting "+key+": dropped") {
			t.Errorf("the report does not drop %s:\n%s", key, got.stdout)
		}
		if n := importCount(t, dest, "SELECT COUNT(*) FROM settings WHERE key = ?", key); n != 0 {
			t.Errorf("%d %s rows survived the import", n, key)
		}
	}
}
