package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestRetiredStoredSettingWarnsAndUnsetRemovesItWithAnAuditRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CONFIG", "")

	if got := runSettings(t, "list"); got.code != cli.ExitOK || strings.Contains(got.stderr, "retired") {
		t.Fatalf("list on a fresh database: exit %d stderr %q", got.code, got.stderr)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('max_run_cost_usd', '0.5')"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	warned := runSettings(t, "list")
	for _, want := range []string{"stored setting is retired", "SETTING=max_run_cost_usd", "VALUE=0.5", "max_run_cost_usd_haiku_5_5", "max_run_cost_usd_opus_5_5"} {
		if !strings.Contains(warned.stderr, want) {
			t.Errorf("stderr %q misses %q", warned.stderr, want)
		}
	}
	if got := runSettings(t, "get", "max_run_cost_usd"); got.code != cli.ExitUsage {
		t.Errorf("get of a retired key: exit %d, want usage error", got.code)
	}

	removed := runSettings(t, "unset", "max_run_cost_usd")
	if removed.code != cli.ExitOK || removed.stdout != "max_run_cost_usd: removed\n" {
		t.Fatalf("unset: exit %d stdout %q stderr %q", removed.code, removed.stdout, removed.stderr)
	}
	if _, ok := storedSetting(t, path, "max_run_cost_usd"); ok {
		t.Error("the orphan row is still stored")
	}
	d, err = db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var old, source string
	if err := d.Read.QueryRow("SELECT old_value, source FROM settings_audit WHERE key = 'max_run_cost_usd'").Scan(&old, &source); err != nil || old != "0.5" || source != "cli" {
		t.Errorf("audit row old=%q source=%q err=%v", old, source, err)
	}
	if again := runSettings(t, "unset", "max_run_cost_usd"); again.code != cli.ExitError {
		t.Errorf("second unset: exit %d, want an error", again.code)
	}
	if after := runSettings(t, "list"); strings.Contains(after.stderr, "retired") {
		t.Errorf("warning persists after removal: %q", after.stderr)
	}
}
