package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestAssistantSettingOrderingAndBuiltinSchedules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_CONFIG", "")

	accepted := func(args ...string) {
		t.Helper()
		if r := runSettings(t, args...); r.code != cli.ExitOK {
			t.Fatalf("%v: exit %d stderr %q", args, r.code, r.stderr)
		}
	}
	refused := func(want string, args ...string) {
		t.Helper()
		r := runSettings(t, args...)
		if r.code != cli.ExitUsage || r.stdout != "" || !strings.Contains(r.stderr, want) {
			t.Errorf("%v: exit %d stdout %q stderr %q, want a refusal containing %q", args, r.code, r.stdout, r.stderr, want)
		}
	}

	accepted("set", "budget_unattended_share", "0.9")
	refused("budget_chat_share must be at least budget_unattended_share (0.9)", "set", "budget_chat_share", "0.5")

	accepted("set", "budget_unattended_share", "0.4")
	accepted("set", "budget_chat_share", "0.5")
	refused("budget_unattended_share must be at most budget_chat_share (0.5)", "set", "budget_unattended_share", "0.9")
	refused("budget_unattended_share must be at most budget_chat_share (0.5)", "unset", "budget_unattended_share")

	accepted("set", "budget_unattended_share", "0.2")
	accepted("set", "budget_chat_share", "0.3")
	refused("budget_unattended_share must be at most budget_chat_share (0.3)", "set", "budget_unattended_share", "0.35")
	accepted("set", "budget_chat_share", "0.5")

	for _, gone := range []string{"agent_clear_share", "agent_compact_share", "agent_preflight_share", "prompt_summary", "agent_schedule_weekly_review", "agent_schedule_media_sweep"} {
		if r := runSettings(t, "set", gone, "0.9"); r.code != cli.ExitUsage || !strings.Contains(r.stderr, `unknown key "`+gone+`"`) {
			t.Errorf("set %s: exit %d stderr %q, want an unknown key refusal", gone, r.code, r.stderr)
		}
	}
}

func TestOwnerScheduleEditOnTheScheduledPageWritesOnlyTheJobRow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_CONFIG", "")

	if r := runSettings(t, "set", "agent_schedule_min_interval_seconds", "3600"); r.code != cli.ExitOK {
		t.Fatalf("set the schedule floor: exit %d stderr %q", r.code, r.stderr)
	}

	w := asstBrowse(t, startServeIn(t, dir))
	code, page, _ := w.get("/scheduled", false)
	w.want(code, http.StatusOK, page, "package-tracking", "0 * * * *", "ops-sampler")

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var id, n int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'package-tracking' AND kind = 'builtin'").Scan(&id); err != nil {
		t.Fatalf("the package-tracking job: %v", err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind IN ('builtin', 'nightly')").Scan(&n); err != nil || n != int64(len(builtinJobNames)) {
		t.Fatalf("%d built-in jobs (%v), want %d", n, err, len(builtinJobNames))
	}
	code, body, _ := w.post("/scheduled/"+strconv.FormatInt(id, 10)+"/schedule", url.Values{"schedule": {"*/50 * * * *"}}, true)
	w.want(code, http.StatusOK, body, "*/50 * * * *")
	var schedule string
	if err := d.Read.QueryRow("SELECT schedule FROM jobs WHERE id = ?", id).Scan(&schedule); err != nil || schedule != "*/50 * * * *" {
		t.Errorf("package-tracking schedule = %q (%v), want the schedule edited on the page", schedule, err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM settings WHERE key = 'agent_schedule_package_tracking'").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d settings rows for the retired schedule key (%v), want none", n, err)
	}
}
