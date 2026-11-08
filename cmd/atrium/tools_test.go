package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestDisabledToolIsNotOffered(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('tool_profile_set_enabled', 'false')"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	ran := runApp("agent", "run", "--text", "hi")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	names := offeredTools(t, api, ran)
	if slices.Contains(names, "profile_set") {
		t.Errorf("disabled profile_set was offered: %v", names)
	}
	if !slices.Contains(names, "profile_get") {
		t.Errorf("enabled profile_get was not offered: %v", names)
	}
}

func lastTools(t *testing.T, api *modelLog, ran appRun) []any {
	t.Helper()
	requests := api.Requests()
	if ran.code != cli.ExitOK || len(requests) == 0 {
		t.Fatalf("agent run: exit %d, %d model requests, stderr %q", ran.code, len(requests), ran.stderr)
	}
	tools, _ := requests[len(requests)-1].JSON(t)["tools"].([]any)
	return tools
}

func TestNightlyRunOmitsWriteUndoTools(t *testing.T) {
	_, api := moduleFixture(t, flagsExcept("", ""))
	ran := runApp("agent", "run", "--text", "hi", "--kind", "nightly")
	names := offeredTools(t, api, ran)
	for _, name := range []string{"profile_set", "ask_operator"} {
		if slices.Contains(names, name) {
			t.Errorf("nightly run was offered the chat-only tool %s: %v", name, names)
		}
	}
	if !slices.Contains(names, "profile_get") {
		t.Errorf("nightly run was not offered the read tool profile_get: %v", names)
	}
}

func TestWebSearchAllowedCallers(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	ran := runApp("agent", "run", "--text", "warm")
	for _, raw := range lastTools(t, api, ran) {
		entry, _ := raw.(map[string]any)
		if entry["name"] != "web_search" {
			continue
		}
		callers, _ := entry["allowed_callers"].([]any)
		if len(callers) != 1 || callers[0] != "direct" {
			t.Fatalf("web_search allowed_callers = %v, want [direct]", entry["allowed_callers"])
		}
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('dynamic_filtering', 'true')"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	ran = runApp("agent", "run", "--text", "hi")
	for _, raw := range lastTools(t, api, ran) {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "web_search" && entry["allowed_callers"] != nil {
			t.Errorf("web_search kept allowed_callers with dynamic filtering: %v", entry["allowed_callers"])
		}
	}
}
