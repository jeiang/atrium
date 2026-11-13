package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeAndTaskPreKUndoPayloadsThroughCLI(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO kb_records (id, title, body, scope, marks, writer, created_at, updated_at) VALUES (900001, 'Changed', 'New body', 'personal', 0, 'operator', 1, 2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO tasks (id, title, status, created_ts, updated_ts) VALUES (900001, 'Legacy task', 'open', 1, 2)"); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		operation, target, payload string
	}{
		{"kb_edit", "kb:900001", `{"op":"kb_restore","id":900001,"before":{"title":"Original","aliases":[],"body":"Old body","scope":"personal","marks":0,"pinned":false,"confirmed_at":null,"retracted_at":null},"applied_updated_at":2}`},
		{"task_add", "task:900001", `{"op":"task_delete","id":900001}`},
	} {
		res, err := d.Write.Exec("INSERT INTO effects (created_at, settled_at, lease_at, token, origin, operation, operation_key, direction, target, undo_json, state) VALUES (1, 2, 1, ?, 'operator', ?, ?, 'forward', ?, ?, 'done')",
			fixture.operation, fixture.operation, fmt.Sprintf("legacy:%s", fixture.operation), fixture.target, fixture.payload)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if ran := runApp(t, "effects", "undo", strconv.FormatInt(id, 10)); ran.code != cli.ExitOK {
			t.Fatalf("undo %s legacy payload: %d %s", fixture.operation, ran.code, ran.stderr)
		}
	}
	var title, body string
	if err := d.Read.QueryRow("SELECT title, body FROM kb_records WHERE id = 900001").Scan(&title, &body); err != nil || title != "Original" || body != "Old body" {
		t.Fatalf("legacy knowledge undo = %q %q (%v)", title, body, err)
	}
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = 900001").Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy task undo left %d tasks (%v)", n, err)
	}
}
