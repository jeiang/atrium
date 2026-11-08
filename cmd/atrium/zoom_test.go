package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestZoomOpensSummariesLeavesAndRefusesBadRanges(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	for i := range 8 {
		content := fmt.Sprintf(`[{"type":"text","text":"message-%d"}]`, i)
		if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (1, ?, 'user', ?, 0)", 3+i, content); err != nil {
			t.Fatal(err)
		}
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory view: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("z1", "zoom", map[string]any{"id": 1, "n": 8})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("z2", "zoom", map[string]any{"id": 3, "n": 1})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("z3", "zoom", map[string]any{"id": 2, "n": 3})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("z4", "zoom", map[string]any{"id": 999, "n": 1})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "zoom"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
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
	for _, want := range []string{`id\": 1`, `to\": 4`, `id\": 5`, `to\": 8`, "message-0", "roots:"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("tool results miss %q: %s", want, all.String())
		}
	}
}
