package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func TestMemoryViewReplacesCompaction(t *testing.T) {
	dir, api := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp("agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	if _, err := d.Write.Exec("UPDATE threads SET context_limit = 20000 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("DELETE FROM memory_nodes WHERE thread_id = 1"); err != nil {
		t.Fatal(err)
	}
	text := `[{"type":"text","text":"` + strings.Repeat("evidence ", 60) + `"}]`
	const count = 200
	for i := range count {
		if _, err := d.Write.Exec("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (1, ?, 'user', ?, 0)", i+3, text); err != nil {
			t.Fatal(err)
		}
	}
	type span struct{ from, to int64 }
	level := make([]span, 0, count+2)
	for s := int64(1); s <= count+2; s++ {
		level = append(level, span{s, s})
	}
	for l := 1; len(level) >= 2; l++ {
		var next []span
		for i := 0; i+1 < len(level); i += 2 {
			from, to := level[i].from, level[i+1].to
			if _, err := d.Write.Exec("INSERT INTO memory_nodes (thread_id, level, idx, text, bytes, state, src_from_seq, src_to_seq, created_at) VALUES (1, ?, ?, 's', 1, 'summary', ?, ?, 0)", l, i/2, from, to); err != nil {
				t.Fatal(err)
			}
			next = append(next, span{from, to})
		}
		level = next
	}
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	ran := runApp("agent", "run", "--thread", "1", "--text", "continue")
	if ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()
	if len(requests) == 0 {
		t.Fatal("no model request")
	}
	body := requests[len(requests)-1].JSON(t)
	if body["context_management"] != nil {
		t.Errorf("request still carries context_management: %v", body["context_management"])
	}
	raw, err := json.Marshal(body["messages"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Memory view") || !strings.Contains(string(raw), "zoom id") {
		t.Errorf("request misses the memory view lines: %s", raw)
	}
	var projections int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM context_projections WHERE thread_id = 1").Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if projections != 0 {
		t.Errorf("wrote %d context_projections rows", projections)
	}
	_ = fmt.Sprintf
}
