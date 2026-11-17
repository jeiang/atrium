package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestKnowledgeConfirmSelectedPageReportsCommittedPrefix(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	c := webG4(t, base)
	ids := make([]int64, 0, 3)
	for _, title := range []string{"First", "Refused", "Later"} {
		made := c.do(http.MethodPost, "/web/v1/knowledge", map[string]any{"title": title, "body": "Keep this fact", "scope": "personal"})
		if made.status != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", title, made.status, made.body)
		}
		ids = append(ids, number(t, dataG4(made)["id"]))
	}
	if got := c.do(http.MethodPost, fmt.Sprintf("/web/v1/knowledge/%d/retract", ids[1]), nil); got.status != http.StatusOK {
		t.Fatalf("retract second record = %d: %.300s", got.status, got.body)
	}
	got := c.do(http.MethodPost, "/web/v1/knowledge/confirm", map[string]any{"ids": ids})
	committed, _ := dataG4(got)["committed"].([]any)
	refused, _ := dataG4(got)["refused"].([]any)
	if got.status != http.StatusOK || len(committed) != 1 || len(refused) == 0 {
		t.Fatalf("committed-prefix refusal = %d: %s", got.status, got.body)
	}
	if first, _ := committed[0].(map[string]any); number(t, first["id"]) != ids[0] {
		t.Fatalf("committed prefix = %s, want the first record", got.body)
	}
	if stopped, _ := refused[0].(map[string]any); number(t, stopped["id"]) != ids[1] {
		t.Fatalf("first refusal = %s, want the retracted record", got.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	for i, id := range ids {
		var confirmed bool
		if err := d.Read.QueryRow("SELECT confirmed_at IS NOT NULL FROM kb_records WHERE id = ?", id).Scan(&confirmed); err != nil || confirmed != (i == 0) {
			t.Fatalf("record %d confirmed = %v (%v), want %v", id, confirmed, err, i == 0)
		}
	}
}
