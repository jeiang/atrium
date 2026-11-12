package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestCanonicalOperatorPinIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")

	setEnv(t, "ATRIUM_MAIL_MAIL_MODE", "live")
	pinned := runSettings(t, "get", "mail_mode")
	if pinned.code != cli.ExitError || !strings.Contains(pinned.stderr, "ATRIUM_MAIL_MAIL_MODE is no longer read") || !strings.Contains(pinned.stderr, "atrium settings set mail_mode") {
		t.Fatalf("canonical operator pin: exit %d stderr %q", pinned.code, pinned.stderr)
	}
}

func TestDatabaseTimeZoneReachesNonServeCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")

	if r := runSettings(t, "set", "tz", "America/Barbados"); r.code != cli.ExitOK {
		t.Fatalf("set tz: exit %d %q", r.code, r.stderr)
	}
	run := func() (int, string) {
		var out, errs bytes.Buffer
		code := defaultApp(t).Run(context.Background(), []string{"shadow-report", "--since", "2026-01-01"}, &out, &errs)
		return code, errs.String()
	}
	if code, stderr := run(); code != cli.ExitOK {
		t.Fatalf("shadow-report with a valid database tz: exit %d %q", code, stderr)
	}

	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Write.Exec("UPDATE settings SET value = 'Mars/Olympus' WHERE key = 'tz'")
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, stderr := run(); code != cli.ExitError || !strings.Contains(stderr, "Mars/Olympus") {
		t.Errorf("shadow-report with a broken database tz: exit %d stderr %q, want it refused naming the zone", code, stderr)
	}
}
