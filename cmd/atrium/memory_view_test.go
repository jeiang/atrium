package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestMemoryViewReducesWithSummaries(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	text := fmt.Sprintf(`[{"type":"text","text":%q}]`, strings.Repeat("x", 400))
	for i := range 400 {
		content := text
		if i == 100 {
			content = `[{"type":"tool_result","tool_use_id":"t","content":"{\"result_id\":42}"}]`
		}
		if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (1, ?, 'user', ?, 0)", 100+i, content); err != nil {
			t.Fatal(err)
		}
	}
	first := runApp(t, "memory", "view", "1")
	if first.code != cli.ExitOK {
		t.Fatalf("first view: %d %s", first.code, first.stderr)
	}
	if !strings.Contains(first.stdout, "over=true") {
		t.Errorf("pending view is not flagged over target: %s", first.stdout)
	}
	if _, err := d.Write.Exec("UPDATE memory_nodes SET state = 'summary', text = 's', bytes = 1 WHERE thread_id = 1 AND level > 0"); err != nil {
		t.Fatal(err)
	}
	second := runApp(t, "memory", "view", "1")
	if second.code != cli.ExitOK {
		t.Fatalf("second view: %d %s", second.code, second.stderr)
	}
	if !strings.Contains(second.stdout, "over=false") {
		t.Errorf("summarized view is still over target: %s", second.stdout)
	}
	if !strings.Contains(second.stdout, "result_ids=42") {
		t.Errorf("the tool-result stub lost its result_id: %s", second.stdout)
	}
	if !strings.Contains(second.stdout, "level 1") {
		t.Errorf("no summary line: %s", second.stdout)
	}
	lines := strings.Split(strings.TrimSpace(second.stdout), "\n")
	for _, line := range lines[len(lines)-6:] {
		if !strings.Contains(line, "level 0") {
			t.Errorf("tail line is not a raw leaf: %s", line)
		}
	}
	if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (1, 700, 'user', ?, 0)", text); err != nil {
		t.Fatal(err)
	}
	fourth := runApp(t, "memory", "view", "1")
	if fourth.code != cli.ExitOK {
		t.Fatalf("fourth view: %d %s", fourth.code, fourth.stderr)
	}
	if strings.Join(strings.Split(strings.TrimSpace(fourth.stdout), "\n")[1:], "\n") == strings.Join(strings.Split(strings.TrimSpace(second.stdout), "\n")[1:], "\n") {
		t.Errorf("a new message did not start a new batch: %s", fourth.stdout)
	}
}
