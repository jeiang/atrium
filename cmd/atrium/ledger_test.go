package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func TestASecondUndoIsRefusedWhileTheFirstRunsAndAPartialUndoBlocksAnother(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	ctx := context.Background()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, path); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	id, _, err := proposals.Create(ctx, d, "test_kind", nil, "title", py.Dict{}, py.Dict{}, "fp", now-100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE proposals SET status = 'applied', applied_ts = ? WHERE id = ?", now-50, id); err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.RecordAction(ctx, d.Write, id, "write", nil, py.Dict{{Key: "op", Value: "move"}},
		py.Dict{{Key: "op", Value: "partial"}}, ledger.OK, nil, now-40); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	registry := proposals.NewRegistry()
	registry.Undoers["partial"] = func(context.Context, json.RawMessage) (py.Dict, error) {
		close(started)
		<-release
		return nil, &mailops.PartialMove{Err: errors.New("expunge failed")}
	}
	pipeline := &proposals.Pipeline{DB: d, Registry: registry, Now: func() float64 { return now }}
	var first proposals.UndoResult
	var firstErr error
	done := make(chan struct{})
	go func() {
		first, firstErr = pipeline.Undo(ctx, id)
		close(done)
	}()
	<-started
	if _, err := pipeline.Undo(ctx, id); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("second undo = %v, want a refusal while the first is running", err)
	}
	close(release)
	<-done
	if firstErr != nil {
		t.Fatalf("first undo = %v", firstErr)
	}
	if first.Undone || len(first.Outcomes) != 1 {
		t.Fatalf("first undo = %+v, want one outcome and not undone", first)
	}
	var rows int
	var state ledger.State
	if err := d.Read.QueryRow("SELECT COUNT(*), COALESCE(MAX(state), '') FROM proposal_actions WHERE proposal_id = ? AND kind = 'undo'", id).
		Scan(&rows, &state); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || state != ledger.UndoPartial {
		t.Fatalf("undo rows = %d state %q, want one partial row", rows, state)
	}
	if _, err := pipeline.Undo(ctx, id); err == nil || !strings.Contains(err.Error(), "partial undo") {
		t.Fatalf("undo after a partial undo = %v, want a refusal", err)
	}
}
