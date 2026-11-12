package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func liveMode(t *testing.T) (string, int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := defaultApp(t).Run(context.Background(), []string{"live", "status"}, &out, &errs)
	if code != cli.ExitOK {
		return "", code, errs.String()
	}
	var status struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	return status.Mode, code, ""
}

func TestMailModeAndWriteCapFollowTheRegistryLive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_CONFIG", "")

	if mode, code, stderr := liveMode(t); code != cli.ExitOK || mode != "shadow" {
		t.Fatalf("default: mode %q exit %d %q", mode, code, stderr)
	}
	if got := runSettings(t, "get", "write_cap").row(t); got.Value == nil || *got.Value != "100" {
		t.Errorf("write_cap default = %v, want 100", got.Value)
	}
	if r := runSettings(t, "set", "write_cap", "1001"); r.code != cli.ExitError || !strings.Contains(r.stderr, "write_cap must be between 1 and 1000") {
		t.Errorf("write_cap 1001: exit %d stderr %q", r.code, r.stderr)
	}

	if r := runSettings(t, "set", "mail_mode", "live"); r.code != cli.ExitOK || !strings.Contains(r.stdout, `"value": "live"`) {
		t.Fatalf("set mail_mode live: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if mode, _, _ := liveMode(t); mode != "live" {
		t.Errorf("stored mode live not read: %q", mode)
	}
	if r := runSettings(t, "set", "mail_mode", "shadow"); r.code != cli.ExitOK || !strings.Contains(r.stdout, `"value": "shadow"`) {
		t.Fatalf("set mail_mode shadow: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if mode, _, _ := liveMode(t); mode != "shadow" {
		t.Errorf("stored mode shadow not read: %q", mode)
	}

	if r := runSettings(t, "set", "mail_mode", "bogus"); r.code != cli.ExitError || !strings.Contains(r.stderr, `must be one of ("shadow", "live"), got "bogus"`) {
		t.Errorf("set mail_mode bogus: exit %d stderr %q", r.code, r.stderr)
	}
	if mode, _, _ := liveMode(t); mode != "shadow" {
		t.Errorf("a refused write changed the mode to %q", mode)
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var audited int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings_audit WHERE key = 'mail_mode' AND actor = 'operator' AND source = 'cli'").Scan(&audited); err != nil || audited != 2 {
		t.Errorf("audited mail_mode writes = %d, %v; want 2", audited, err)
	}

	plantSettingRow(t, path, "mail_mode", "bogus")
	if _, code, stderr := liveMode(t); code != cli.ExitError || !strings.Contains(stderr, `must be one of ("shadow", "live"), got "bogus"`) {
		t.Errorf("invalid stored mode: exit %d stderr %q", code, stderr)
	}
}
