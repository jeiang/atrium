package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
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

func TestRetiredSettingEnvironmentVariableWarns(t *testing.T) {
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_ASSISTANT_NIGHTLY_STALE_MONTHS", "3")
	logs := &lockedBuffer{}
	previous := slog.Default()
	logx.Setup(logs)
	t.Cleanup(func() { slog.SetDefault(previous) })
	if r := runSettings(t, "list"); r.code != cli.ExitOK {
		t.Fatalf("settings list with a retired variable: exit %d stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(logs.String(), "ignoring a removed setting") || !strings.Contains(logs.String(), "ATRIUM_ASSISTANT_NIGHTLY_STALE_MONTHS") {
		t.Errorf("the retired variable was not warned about: %q", logs.String())
	}
}

func TestRemovedPhoneActionSettingsDoNotBlockStartUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	if r := runSettings(t, "set", "tz", "America/Barbados"); r.code != cli.ExitOK {
		t.Fatalf("set tz: exit %d %q", r.code, r.stderr)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Write.Exec("INSERT INTO settings (key, value) VALUES ('phone_actions_enabled', 'true'), ('phone_actions_kinds', 'reminder')")
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_ACTION_LINKS_PHONE_ACTIONS_ENABLED", "true")
	setEnv(t, "ATRIUM_ACTION_LINKS_PHONE_ACTIONS_KINDS", "reminder")
	logs := &lockedBuffer{}
	previous := slog.Default()
	logx.Setup(logs)
	t.Cleanup(func() { slog.SetDefault(previous) })
	listed := runSettings(t, "list")
	if listed.code != cli.ExitOK {
		t.Fatalf("settings list with removed phone action settings: exit %d stderr %q", listed.code, listed.stderr)
	}
	if strings.Contains(listed.stdout, "phone_actions") {
		t.Errorf("settings list still shows a phone action setting: %q", listed.stdout)
	}
	for _, name := range []string{"ATRIUM_ACTION_LINKS_PHONE_ACTIONS_ENABLED", "ATRIUM_ACTION_LINKS_PHONE_ACTIONS_KINDS"} {
		if !strings.Contains(logs.String(), name) {
			t.Errorf("the removed variable %s was not warned about: %q", name, logs.String())
		}
	}
	var out, errs bytes.Buffer
	if code := defaultApp(t).Run(context.Background(), []string{"shadow-report", "--since", "2026-01-01"}, &out, &errs); code != cli.ExitOK {
		t.Fatalf("a command with removed phone action settings stored and pinned: exit %d %q", code, errs.String())
	}
}

func TestFileSpellingOfACutoverKeyNamesTheFileVariable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")

	setEnv(t, "ATRIUM_MAIL_MAIL_MODE_FILE", filepath.Join(dir, "mode"))
	r := runSettings(t, "get", "mail_mode")
	if r.code != cli.ExitError || !strings.Contains(r.stderr, "ATRIUM_MAIL_MAIL_MODE_FILE is no longer read") || !strings.Contains(r.stderr, "atrium settings set mail_mode") {
		t.Fatalf("file spelling of a cut-over key: exit %d stderr %q", r.code, r.stderr)
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
