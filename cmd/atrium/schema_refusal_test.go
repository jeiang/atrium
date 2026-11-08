package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestCommandRefusesNewerSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(t.Context(), d, path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 1.0); DELETE FROM subagents WHERE name = 'web'"); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))

	cmd := cliExitProcess(t, "loops refresh")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := exitCode(cmd.Run())
	if code != cli.ExitError || stdout.Len() != 0 || !strings.Contains(stderr.String(), "does not know") || !strings.Contains(stderr.String(), "9999") {
		t.Errorf("loops refresh on a newer schema exited %d, stdout %q, stderr %q; want %d, empty stdout and the unknown-migration message", code, stdout.String(), stderr.String(), cli.ExitError)
	}
	var subagents int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM subagents WHERE name = 'web'").Scan(&subagents); err != nil {
		t.Fatal(err)
	}
	if subagents != 0 {
		t.Error("the refused start re-seeded the database")
	}
}
