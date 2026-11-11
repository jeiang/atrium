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
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('tool_kb_search_enabled', 'false')"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	ran := runApp(t, "agent", "run", "--text", "hi")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	names := offeredTools(t, api, ran)
	if slices.Contains(names, "kb_search") {
		t.Errorf("disabled kb_search was offered: %v", names)
	}
	if !slices.Contains(names, "kb_get") {
		t.Errorf("enabled kb_get was not offered: %v", names)
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
	_, api, _ := moduleFixture(t, flagsExcept("", ""))
	ran := runApp(t, "agent", "run", "--text", "hi", "--kind", "nightly")
	names := offeredTools(t, api, ran)
	for _, name := range []string{"ask_operator"} {
		if slices.Contains(names, name) {
			t.Errorf("nightly run was offered the chat-only tool %s: %v", name, names)
		}
	}
	if !slices.Contains(names, "kb_get") {
		t.Errorf("nightly run was not offered the read tool kb_get: %v", names)
	}
}

func TestWebSearchAllowedCallers(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	script := func() {
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_web", map[string]any{"query": "atrium"})))
		api.Push(anthropictest.Reply("pause_turn", anthropictest.WebSearch("ws1", "atrium", anthropictest.SearchResult{URL: "https://example.com/a", Title: "A"})...))
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "report", map[string]any{"answer": map[string]any{"answer": "", "citations": []any{}}})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("ok")))
	}
	start := warm(t, api)
	script()
	if ran := runApp(t, "agent", "run", "--text", "search"); ran.code != cli.ExitOK {
		t.Fatalf("search run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 5 {
		t.Fatalf("%d model requests, want 5", len(requests))
	}
	assertServerCallers(t, requests[1], "direct")
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('search_web_dynamic_filtering', 'true')"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	start = len(api.Requests())
	script()
	if ran := runApp(t, "agent", "run", "--text", "search again"); ran.code != cli.ExitOK {
		t.Fatalf("second search run: %d %s", ran.code, ran.stderr)
	}
	requests = api.Requests()[start:]
	if len(requests) != 5 {
		t.Fatalf("%d model requests, want 5", len(requests))
	}
	assertServerCallers(t, requests[1], "direct")
	if got := runSettings(t, "set", "search_web_dynamic_filtering", "true"); got.code != cli.ExitUsage || !strings.Contains(got.stderr, "unknown key") {
		t.Errorf("set search_web_dynamic_filtering: exit %d stderr %q, want the key refused as a compiled constant", got.code, got.stderr)
	}
}

func assertServerCallers(t *testing.T, r anthropictest.Request, want string) {
	t.Helper()
	for _, raw := range r.JSON(t)["tools"].([]any) {
		entry, _ := raw.(map[string]any)
		if entry["name"] != "web_search" {
			continue
		}
		callers, _ := entry["allowed_callers"].([]any)
		if want == "" {
			if len(callers) != 0 {
				t.Errorf("web_search allowed_callers = %v, want none", callers)
			}
			return
		}
		if len(callers) != 1 || callers[0] != want {
			t.Errorf("web_search allowed_callers = %v, want [%s]", callers, want)
		}
		return
	}
	t.Fatal("the child request has no web_search server tool")
}

func TestFileToolsReadSearchAndTraversal(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f1", "file_write", map[string]any{"name": "note.txt", "content": "hello world"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f2", "file_read", map[string]any{"name": "note.txt"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f3", "file_read", map[string]any{"name": "../secret"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("f4", "file_search", map[string]any{"query": "hello"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp(t, "agent", "run", "--thread", "1", "--text", "write a note")
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

func TestMemorySpendCountsTowardTheMonthlyBudget(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
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
	month := asstMonth(t, now)
	insert := "INSERT INTO usage (attempt_key, ts, month, expires_at, settled_at, state, budget_class, reservation_nusd, accounted_nusd, request_id, source, provider, thread_id, run_id, kind, model, effort, iterations, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, 'settled', ?, 0, ?, lower(hex(randomblob(16))), 'loop', 'anthropic', 1, 1, ?, 'm', 'low', '[]', 0, 0, 0, 0, 0, 0, 'low', ?)"
	seed := func(kind, class string, usd float64) {
		t.Helper()
		if _, err := d.Write.Exec(insert, now, month, now, now, class, int64(usd*1e9), kind, usd); err != nil {
			t.Fatal(err)
		}
	}
	seed("chat", "interactive", 1.0)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "hi"); !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("chat did not pause at the threshold: code=%d stdout=%s stderr=%s", ran.code, ran.stdout, ran.stderr)
	}
	seed("memory", "unattended", 100.0)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "hi"); !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("a memory row changed the pause decision: code=%d stdout=%s stderr=%s", ran.code, ran.stdout, ran.stderr)
	}
	if _, err := d.Write.Exec("UPDATE usage SET cost_usd = 0.5, accounted_nusd = 500000000 WHERE kind = 'chat'"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "hi"); !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("memory spend did not count toward the budget: code=%d stdout=%s stderr=%s", ran.code, ran.stdout, ran.stderr)
	}
	if _, err := d.Write.Exec("DELETE FROM usage WHERE kind = 'memory'"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "hi"); ran.code != cli.ExitOK {
		t.Fatalf("chat stayed paused without the memory row: %d %s", ran.code, ran.stderr)
	}
}
