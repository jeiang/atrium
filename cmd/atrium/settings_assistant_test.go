package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestAssistantSettingOrderingAndBuiltinSchedules(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	t.Setenv("ATRIUM_CONFIG", "")

	accepted := func(args ...string) {
		t.Helper()
		if r := runSettings(t, args...); r.code != cli.ExitOK {
			t.Fatalf("%v: exit %d stderr %q", args, r.code, r.stderr)
		}
	}
	refused := func(want string, args ...string) {
		t.Helper()
		r := runSettings(t, args...)
		if r.code != cli.ExitError || r.stdout != "" || !strings.Contains(r.stderr, want) {
			t.Errorf("%v: exit %d stdout %q stderr %q, want a refusal containing %q", args, r.code, r.stdout, r.stderr, want)
		}
	}

	accepted("set", "budget_unattended_share", "0.9")
	refused("budget_chat_share must be at least budget_unattended_share (0.9)", "set", "budget_chat_share", "0.5")

	accepted("set", "budget_unattended_share", "0.4")
	accepted("set", "budget_chat_share", "0.5")
	refused("budget_unattended_share must be at most budget_chat_share (0.5)", "set", "budget_unattended_share", "0.9")
	refused("budget_unattended_share must be at most budget_chat_share (0.5)", "unset", "budget_unattended_share")

	t.Setenv("ATRIUM_ASSISTANT_BUDGET_CHAT_SHARE", "0.3")
	refused("budget_unattended_share must be at most budget_chat_share (0.3)", "set", "budget_unattended_share", "0.35")
	t.Setenv("ATRIUM_ASSISTANT_BUDGET_CHAT_SHARE", "")

	refused("agent_clear_share must be below agent_compact_share (0.875)", "set", "agent_clear_share", "0.9")
	refused("agent_preflight_share must be above agent_compact_share (0.875)", "set", "agent_preflight_share", "0.8")
	accepted("set", "agent_compact_share", "0.9")
	refused("agent_compact_share must be below agent_preflight_share (0.95)", "set", "agent_compact_share", "0.96")
	refused("agent_schedule_daily_nudges", "set", "agent_schedule_daily_nudges", "*/5 * * * *")

	base := startServeIn(t, dir)
	_, page, _ := fetch(t, http.DefaultClient, "GET", base+"/scheduled", nil)
	if !strings.Contains(page, "*/10 * * * *") || strings.Contains(page, "*/20 * * * *") {
		t.Fatalf("GET /scheduled before the override should list the default media sweep schedule: %.800s", page)
	}
	accepted("set", "agent_schedule_media_sweep", "*/20 * * * *")
	_, page, _ = fetch(t, http.DefaultClient, "GET", base+"/scheduled", nil)
	if !strings.Contains(page, "*/20 * * * *") {
		t.Errorf("GET /scheduled after the override should list the new media sweep schedule: %.800s", page)
	}
}
