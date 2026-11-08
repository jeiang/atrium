package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func toolResultContents(t *testing.T, dir string) []string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	rows, err := d.Read.Query("SELECT content_json FROM thread_messages WHERE thread_id = 1 AND role = 'user' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var blocks []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
			t.Fatal(err)
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				out = append(out, b.Content)
			}
		}
	}
	return out
}

func TestSearchThreadsFindsSummariesAcrossThreads(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("INSERT INTO projects (id, name, effort, context_limit, created_at, updated_at) VALUES (1, 'one', 'low', 100000, 0, 0), (2, 'two', 'low', 100000, 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO threads (id, kind, title, effort, created_at, updated_at, project_id) VALUES (2, 'chat', 'old', 'low', 0, 0, 1), (3, 'chat', 'other', 'low', 0, 0, 2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, created_at) VALUES (2, 1, 0, 'the secret fact is blue', 24, 'summary', 10, 11, 0), (3, 1, 0, 'the secret fact is red', 23, 'summary', 20, 21, 0)"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "search_threads", map[string]any{"query": "secret"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s2", "search_threads", map[string]any{"query": "secret", "project": 1})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s3", "search_threads", map[string]any{"query": "secret", "limit": 1})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp("agent", "run", "--thread", "1", "--text", "search"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	results := toolResultContents(t, dir)
	if len(results) != 3 {
		t.Fatalf("%d tool results, want 3", len(results))
	}
	for _, want := range []string{`"thread_id": 2`, `"from": 10`, `"to": 11`, "blue", "/chat/2", `"thread_id": 3`, "red"} {
		if !strings.Contains(results[0], want) {
			t.Errorf("unfiltered hit misses %q: %s", want, results[0])
		}
	}
	if strings.Contains(results[0], "message body") {
		t.Errorf("search leaked a message body: %s", results[0])
	}
	if !strings.Contains(results[1], `"thread_id": 2`) || strings.Contains(results[1], `"thread_id": 3`) {
		t.Errorf("project filter did not narrow: %s", results[1])
	}
	if strings.Count(results[2], `"thread_id"`) != 1 {
		t.Errorf("limit was not honored: %s", results[2])
	}
}
