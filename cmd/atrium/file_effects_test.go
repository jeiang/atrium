package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func TestFileWriteUndoRemovesOnlyTheParentsItCreated(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	keep := filepath.Join(dir, "files", "keep")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "mine.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "file_write", map[string]any{"name": "note.txt", "content": "hello"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "write the note"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	note := filepath.Join(dir, "files", "1", "note.txt")
	if data, err := os.ReadFile(note); err != nil || string(data) != "hello" {
		t.Fatalf("note.txt = %q, %v", data, err)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE direction = 'forward' AND operation = 'file_write' AND state = 'done' ORDER BY id DESC LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("file effect: %v", err)
	}
	var audits int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM operation_audit WHERE operation = 'assistant.file.write' AND outcome = 'ok' AND target = 'thread:1' AND details_json = ?", fmt.Sprintf(`{"effect_ids":[%d]}`, id)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("file write audits = %d, %v; want one naming effect %d", audits, err, id)
	}
	if ran := runApp(t, "effects", "undo", fmt.Sprint(id)); ran.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %s", ran.code, ran.stderr)
	}
	if _, err := os.Stat(note); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file survived its undo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the undo left the created thread directory: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(keep, "mine.txt")); err != nil || string(data) != "keep" {
		t.Errorf("the undo touched an unrelated file: %q, %v", data, err)
	}
}
