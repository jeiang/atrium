package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func liveMode(t *testing.T) (string, int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := newApp().Run(context.Background(), []string{"live", "status"}, &out, &errs)
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
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	t.Setenv("ATRIUM_CONFIG", "")
	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "")

	if mode, code, stderr := liveMode(t); code != cli.ExitOK || mode != "shadow" {
		t.Fatalf("default: mode %q exit %d %q", mode, code, stderr)
	}
	if got := runSettings(t, "get", "write_cap").row(t); got.Value == nil || *got.Value != "100" {
		t.Errorf("write_cap default = %v, want 100", got.Value)
	}
	if r := runSettings(t, "set", "write_cap", "1001"); r.code != cli.ExitError || !strings.Contains(r.stderr, "write_cap must be between 1 and 1000") {
		t.Errorf("write_cap 1001: exit %d stderr %q", r.code, r.stderr)
	}
	if r := runSettings(t, "set", "mail_mode", "live"); r.code != cli.ExitError {
		t.Errorf("set mail_mode live: exit %d stderr %q, want a refusal", r.code, r.stderr)
	}
	if mode, _, _ := liveMode(t); mode != "shadow" {
		t.Errorf("a refused write changed the mode to %q", mode)
	}

	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "live")
	if mode, _, _ := liveMode(t); mode != "live" {
		t.Errorf("env pin live not read: %q", mode)
	}
	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "shadow")
	if mode, _, _ := liveMode(t); mode != "shadow" {
		t.Errorf("env pin shadow not read: %q", mode)
	}
	if r := runSettings(t, "unset", "mail_mode"); r.code != cli.ExitError {
		t.Errorf("unset mail_mode: exit %d, want a refusal", r.code)
	}

	t.Setenv("ATRIUM_MAIL_MAIL_MODE", "bogus")
	if _, code, stderr := liveMode(t); code != cli.ExitError || !strings.Contains(stderr, "must be one of ('shadow', 'live'), got 'bogus'") {
		t.Errorf("invalid pin: exit %d stderr %q", code, stderr)
	}
}
