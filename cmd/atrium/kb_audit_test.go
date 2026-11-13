package main

import (
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeWriteAuditCarriesTrustedInvocation(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("write", "kb_write", map[string]any{
		"title": "Boiler", "body": "The switch is downstairs.", "scope": "work",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("saved")))
	if ran := runApp(t, "agent", "run", "--text", "remember the boiler switch"); ran.code != cli.ExitOK {
		t.Fatalf("kb_write run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit a JOIN runs r ON r.id = a.run_id JOIN kb_records k ON CAST(k.id AS TEXT) = a.target WHERE a.operation = 'knowledge.agent-write' AND a.outcome = 'ok' AND a.thread_id = r.thread_id AND a.thread_id > 0 AND k.title = 'Boiler'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("knowledge write audit with trusted invocation = %d (%v), want 1", n, err)
	}
}
