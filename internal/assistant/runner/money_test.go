package runner_test

import (
	"testing"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/toolset"
)

func TestPayeeRenameThroughTheLoopIsLoggedAndUndoable(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	if _, err := w.d.Write.ExecContext(ctx, "INSERT INTO payees (id, name, created_ts) VALUES (7, 'Old Name', 1)"); err != nil {
		t.Fatal(err)
	}
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "rename it",
		toolTurn(use("tu1", "payee_rename", map[string]any{"payee_id": 7, "name": "New Name"})),
		reply("done"),
	)
	result := decoded(t, resultOf(t, w.request(1), "tu1"))
	writeID, ok := result["write_id"].(float64)
	if !ok || result["payee_id"] != float64(7) || result["name"] != "New Name" {
		t.Fatalf("result %v", result)
	}
	var tool, target string
	if err := w.d.Read.QueryRowContext(ctx, "SELECT tool, target FROM agent_writes WHERE id = ?", int64(writeID)).Scan(&tool, &target); err != nil {
		t.Fatal(err)
	}
	if tool != "payee_rename" || target != "payee:7" {
		t.Errorf("write %q %q", tool, target)
	}
	if _, err := toolset.Undoers(w.env).Undo(ctx, w.d, int64(writeID), w.svc.Now); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := w.d.Read.QueryRowContext(ctx, "SELECT name FROM payees WHERE id = 7").Scan(&name); err != nil || name != "Old Name" {
		t.Errorf("name after undo %q, %v", name, err)
	}
}
