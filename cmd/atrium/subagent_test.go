package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/inference"
)

func systemText(t *testing.T, r anthropictest.Request) string {
	t.Helper()
	var b strings.Builder
	blocks, _ := r.JSON(t)["system"].([]any)
	for _, raw := range blocks {
		if entry, ok := raw.(map[string]any); ok {
			text, _ := entry["text"].(string)
			b.WriteString(text)
		}
	}
	return b.String()
}

func research(task string) anthropictest.Response {
	return anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "research", map[string]any{"role": "mail", "task": task}))
}

func TestSubagentRequestOmitsTheOperatorProfile(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	const home = "Maple Street 12"
	warm(t, api)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "kb_write", map[string]any{"title": "home", "body": home})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--text", "remember where I live"); ran.code != cli.ExitOK {
		t.Fatalf("profile run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("UPDATE kb_records SET pinned = 1, confirmed_at = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	start := len(api.Requests())
	api.Push(research("find the library opening hours"))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("open 9-5")))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--text", "when is the library open"); ran.code != cli.ExitOK {
		t.Fatalf("research run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 3 {
		t.Fatalf("%d model requests, want 3", len(requests))
	}
	if parent := systemText(t, requests[0]); !strings.Contains(parent, home) {
		t.Fatalf("parent system prompt lacks the profile: %q", parent)
	}
	child := systemText(t, requests[1])
	if !strings.Contains(child, "Agent `mail`") {
		t.Fatalf("second request is not the subagent: %q", child)
	}
	if strings.Contains(child, home) || strings.Contains(child, "Operator profile") {
		t.Errorf("subagent system prompt carries the profile: %q", child)
	}
}

func runError(t *testing.T, dir, where string, args ...any) string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var text *string
	if err := d.Read.QueryRow("SELECT error FROM runs WHERE "+where+" ORDER BY id DESC LIMIT 1", args...).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text == nil {
		return ""
	}
	return *text
}

func TestSubagentSpendCountsTowardTheParentRunCap(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	seedSettings(t, map[string]string{assistant.KeyDefaultModel: pricing.HaikuID})
	setEnv(t, settingEnv(inference.RunCapKey(pricing.HaikuID)), "0.05")
	over := anthropictest.Usage{Input: 1000, Output: 120_000}

	start := warm(t, api)
	api.Push(research("look this up"))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("expensive digest")).WithUsage(over))
	runApp(t, "agent", "run", "--text", "research something")
	if sent := len(api.Requests()) - start; sent != 2 {
		t.Fatalf("%d model requests after the subagent spent past the parent cap, want 2", sent)
	}
	if text := runError(t, dir, "parent_run_id IS NULL AND kind = 'chat'"); !strings.Contains(text, "run cost") {
		t.Errorf("parent run error %q, want a run cost stop", text)
	}
	start = len(api.Requests())
	api.Push(research("look this up too").WithUsage(over))
	runApp(t, "agent", "run", "--text", "research again")
	if sent := len(api.Requests()) - start; sent != 1 {
		t.Fatalf("%d model requests, want only the parent's", sent)
	}
	if text := runError(t, dir, "parent_run_id IS NOT NULL"); !strings.Contains(text, "parent run cost cap") {
		t.Errorf("child run error %q, want a parent run cost cap refusal", text)
	}
}
