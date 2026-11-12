package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestMemoryTreeFollowsChatRuns(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	reply := anthropictest.Reply("end_turn", anthropictest.Text("hi"))
	api.Push(reply, reply)
	ran := runApp(t, "agent", "run", "--text", "one")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(ran.stdout), &out); err != nil {
		t.Fatalf("agent run output: %v", err)
	}
	thread := int64(out["thread_id"].(float64))
	for _, text := range []string{"two", "three"} {
		ran = runApp(t, "agent", "run", "--thread", fmt.Sprint(thread), "--text", text)
		if ran.code != cli.ExitOK {
			t.Fatalf("agent run %s: %d %s", text, ran.code, ran.stderr)
		}
	}
	tree := runApp(t, "memory", "tree", fmt.Sprint(thread))
	if tree.code != cli.ExitOK {
		t.Fatalf("memory tree: %d %s", tree.code, tree.stderr)
	}
	for _, want := range []string{
		"thread " + fmt.Sprint(thread) + ": 10 nodes",
		"root level=2 idx=0 seq=1..4 state=direct",
		"root level=1 idx=2 seq=5..6 state=direct",
	} {
		if !strings.Contains(tree.stdout, want) {
			t.Errorf("memory tree output lacks %q:\n%s", want, tree.stdout)
		}
	}
	if strings.Contains(tree.stdout, "root level=2 idx=1") {
		t.Errorf("memory tree has an unexpected second level-2 root:\n%s", tree.stdout)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	var leaves int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", thread).Scan(&leaves); err != nil {
		t.Fatal(err)
	}
	if leaves != 6 {
		t.Errorf("leaf count = %d, want 6", leaves)
	}
	var messages int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 6 {
		t.Errorf("active messages = %d, want 6", messages)
	}
}
