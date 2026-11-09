package main

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
)

func TestProposalUndoIsRefusedWhileAnUndoRunsOrAfterAPartialUndo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	ctx := settings.With(context.Background(), settings.New())
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	applied := func(name string, undo ledger.State, text *string) int64 {
		id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, name, map[string]any{}, map[string]any{}, name, now-100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'applied', decided_ts = ?, applied_ts = ? WHERE id = ?", now-60, now-50, id); err != nil {
			t.Fatal(err)
		}
		write, err := proposals.RecordAction(ctx, seed.Write, id, "write", nil, map[string]any{"op": "move"},
			map[string]any{"op": live.OpMoveBack}, ledger.OK, nil, now-40)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proposals.RecordAction(ctx, seed.Write, id, "undo", &write, map[string]any{"op": proposals.UndoOp, "of": write},
			nil, undo, text, now); err != nil {
			t.Fatal(err)
		}
		return id
	}
	partialText := mailops.PartialPrefix + "expunge failed"
	running := applied("running", ledger.Held, nil)
	partial := applied("partial", ledger.UndoPartial, &partialText)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	if got := runApp(t, "proposals", "undo", id(running)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "in progress") {
		t.Fatalf("undo while an undo runs = %d %q %q, want a refusal", got.code, got.stdout, got.stderr)
	}
	if got := runApp(t, "proposals", "undo", id(partial)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "partial undo") {
		t.Fatalf("undo after a partial undo = %d %q %q, want a refusal", got.code, got.stdout, got.stderr)
	}

	check := mustConnect(t, path)
	defer func() { _ = check.Close() }()
	for _, pid := range []int64{running, partial} {
		var undos int
		var status string
		var undone *float64
		if err := check.Read.QueryRow("SELECT (SELECT COUNT(*) FROM proposal_actions WHERE proposal_id = p.id AND kind = 'undo'), status, undone_ts FROM proposals p WHERE id = ?", pid).
			Scan(&undos, &status, &undone); err != nil {
			t.Fatal(err)
		}
		if undos != 1 || status != proposals.Applied || undone != nil {
			t.Errorf("proposal %d after a refused undo: %d undo rows, status %q, undone %v; want it untouched", pid, undos, status, undone)
		}
	}
}
