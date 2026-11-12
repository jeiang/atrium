package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestMemoryBackfillIsIdempotentAndRefusesAfterDone(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	if _, err := d.Write.Exec("INSERT INTO threads (id, kind, title, effort, created_at, updated_at) VALUES (2, 'chat', 'older', 'low', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	long := `[{"type":"text","text":"` + strings.Repeat("evidence ", 100) + `"}]`
	for i := range 4 {
		if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (2, ?, 'user', ?, 0)", i+1, long); err != nil {
			t.Fatal(err)
		}
	}
	estimate := runApp(t, "memory", "backfill", "--estimate")
	if estimate.code != cli.ExitOK {
		t.Fatalf("estimate: %d %s", estimate.code, estimate.stderr)
	}
	if !strings.Contains(estimate.stdout, "threads: 2") || !strings.Contains(estimate.stdout, "estimated cost:") {
		t.Errorf("estimate output: %s", estimate.stdout)
	}
	var usageRows int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE kind = 'memory'").Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 0 {
		t.Errorf("estimate recorded %d usage rows", usageRows)
	}
	for range 100 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	if ran := runApp(t, "memory", "backfill"); ran.code != cli.ExitOK {
		t.Fatalf("backfill: %d %s", ran.code, ran.stderr)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE kind = 'memory'").Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows == 0 {
		t.Error("backfill recorded no memory usage")
	}
	var done string
	if err := d.Read.QueryRow("SELECT value FROM memory_state WHERE key = 'backfill_done'").Scan(&done); err != nil {
		t.Fatalf("backfill_done: %v", err)
	}
	if done == "" {
		t.Error("backfill_done is empty")
	}
	again := runApp(t, "memory", "backfill")
	if again.code == cli.ExitOK || !strings.Contains(again.stderr, "already completed") {
		t.Errorf("second backfill: %d %s", again.code, again.stderr)
	}
	var before int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 2").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "memory", "backfill", "--thread", "2"); ran.code != cli.ExitOK {
		t.Fatalf("thread backfill: %d %s", ran.code, ran.stderr)
	}
	var after int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = 2").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("rerun duplicated nodes: %d -> %d", before, after)
	}
}
