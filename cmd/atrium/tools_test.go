package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestDisabledToolIsNotOffered(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('tool_profile_set_enabled', 'false')"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	ran := runApp(t, "agent", "run", "--text", "hi")
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

func TestFileToolsReadSearchAndTraversal(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "file_write", map[string]any{"name": "note.txt", "content": "hello world"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "file_read", map[string]any{"name": "note.txt"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f3", "file_read", map[string]any{"name": "../secret"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f4", "file_search", map[string]any{"query": "hello"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp("agent", "run", "--thread", "1", "--text", "write a note")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "files", "1", "note.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("note.txt = %q, %v; run: %s", data, err, ran.stdout)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	rows, err := d.Read.Query("SELECT content_json FROM thread_messages WHERE thread_id = 1 AND role = 'user' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var all strings.Builder
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatal(err)
		}
		all.WriteString(content)
	}
	for _, want := range []string{"hello world", "invalid file name", "note.txt: hello world"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("tool results miss %q: %s", want, all.String())
		}
	}
}

func TestMemorySpendDoesNotPauseChat(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("DELETE FROM usage"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('monthly_budget_usd', '1')"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	insert := "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) VALUES (?, 1, 1, ?, 'm', 'low', 0, 0, 0, 0, 0, 0, 'low', ?)"
	if _, err := d.Write.Exec(insert, now, "chat", 1.0); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp("agent", "run", "--thread", "1", "--text", "hi"); !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("chat did not pause at the threshold: code=%d stdout=%s stderr=%s", ran.code, ran.stdout, ran.stderr)
	}
	if _, err := d.Write.Exec(insert, now, "memory", 100.0); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp("agent", "run", "--thread", "1", "--text", "hi"); !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("a memory row changed the pause decision: code=%d stdout=%s stderr=%s", ran.code, ran.stdout, ran.stderr)
	}
	if _, err := d.Write.Exec("UPDATE usage SET cost_usd = 0.5 WHERE kind = 'chat'"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp("agent", "run", "--thread", "1", "--text", "hi"); ran.code != cli.ExitOK {
		t.Fatalf("chat stayed paused with a large memory row: %d %s", ran.code, ran.stderr)
	}
}
