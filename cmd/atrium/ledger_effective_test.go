package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

func effectiveFixture(t *testing.T) (string, *db.DB) {
	t.Helper()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "settings", "list"); ran.code != cli.ExitOK {
		t.Fatalf("settings list: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	t.Cleanup(func() { _ = d.Close() })
	return dir, d
}

func effectiveProposal(t *testing.T, d *db.DB, name string, now float64) int64 {
	t.Helper()
	ctx := testProposalContext()
	id, _, err := proposals.Create(ctx, d, "deletion_rule", nil, "Add a task", map[string]any{}, map[string]any{}, name, now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolvedAppliedProposalWorkBlocksReapproval(t *testing.T) {
	_, d := effectiveFixture(t)
	now := float64(time.Now().Unix())
	id := effectiveProposal(t, d, "effective-reapproval", now)
	effectID := seedEffect(t, d, seededEffect{
		Origin: "proposal", Operation: "task_add", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":task_add",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
		Action: `{"op":"task_add"}`, Undo: `{"op":"task_delete","id":1}`, State: "unknown", CreatedAt: now, LeaseAt: now, Settled: true,
	})
	if _, err := d.Write.Exec("INSERT INTO effect_resolutions (effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, prior_state, prior_error) "+
		"VALUES (?, ?, ?, 'operator', 'cli', 'applied', 'done', 'unknown', '')", effectID, id, now); err != nil {
		t.Fatal(err)
	}
	ran := runApp(t, "proposals", "approve", strconv.FormatInt(id, 10))
	if ran.code == cli.ExitOK {
		t.Fatalf("a proposal with resolved-applied work was approved again: %s", ran.stdout)
	}
	if !strings.Contains(ran.stdout+ran.stderr, "already has a completed write") {
		t.Fatalf("the refusal = %q, want the completed-work refusal", ran.stdout+ran.stderr)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != proposals.Pending {
		t.Fatalf("proposal status = %q, want pending", status)
	}
}

func TestProposalUndoIncludesResolvedAppliedAndStopsOnAFailedUndo(t *testing.T) {
	_, d := effectiveFixture(t)
	now := float64(time.Now().Unix())
	id := effectiveProposal(t, d, "effective-undo", now)
	if _, err := d.Write.Exec("UPDATE proposals SET status = 'applied', decided_ts = ?, applied_ts = ? WHERE id = ?", now-60, now-50, id); err != nil {
		t.Fatal(err)
	}
	res, err := d.Write.Exec("INSERT INTO tasks (title, status, created_ts, updated_ts) VALUES ('Undo me', 'open', ?, ?)", now, now)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	seedEffect(t, d, seededEffect{
		Origin: "proposal", Operation: "task_add", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":task_add",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
		Action: `{"op":"task_add"}`, Undo: fmt.Sprintf(`{"op":"task_delete","id":%d}`, taskID), State: "done", CreatedAt: now - 40, LeaseAt: now - 40, Settled: true,
	})
	changed := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(changed, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer := seedEffect(t, d, seededEffect{
		Origin: "proposal", Operation: "obsidian_write", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":obsidian_write",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
		Action: `{"path":"note.md"}`, Undo: fmt.Sprintf(`{"op":"obsidian_restore","path":%q,"before":"original","sha":%q}`, changed, strings.Repeat("0", 64)),
		State: "unknown", CreatedAt: now, LeaseAt: now, Settled: true,
	})
	if _, err := d.Write.Exec("INSERT INTO effect_resolutions (effect_id, proposal_id, ts, actor, source, outcome, effective_outcome, prior_state, prior_error) "+
		"VALUES (?, ?, ?, 'operator', 'cli', 'applied', 'done', 'unknown', '')", newer, id, now); err != nil {
		t.Fatal(err)
	}
	ran := runApp(t, "proposals", "undo", strconv.FormatInt(id, 10))
	if ran.code != cli.ExitOK {
		t.Fatalf("proposals undo: %d %s", ran.code, ran.stderr)
	}
	if !strings.Contains(ran.stdout, `"undone": false`) {
		t.Fatalf("undo result = %s, want undone false after a failed inverse", ran.stdout)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatal("a failed newer inverse did not stop the reverse-order walk")
	}
	var undone *float64
	if err := d.Read.QueryRow("SELECT undone_ts FROM proposals WHERE id = ?", id).Scan(&undone); err != nil {
		t.Fatal(err)
	}
	if undone != nil {
		t.Fatalf("the proposal was stamped undone at %v despite a failed inverse", *undone)
	}
}

func TestProposalUndoReadsEveryEffect(t *testing.T) {
	_, d := effectiveFixture(t)
	now := float64(time.Now().Unix())
	id := effectiveProposal(t, d, "effective-pagination", now)
	if _, err := d.Write.Exec("UPDATE proposals SET status = 'applied', decided_ts = ?, applied_ts = ? WHERE id = ?", now-60, now-50, id); err != nil {
		t.Fatal(err)
	}
	res, err := d.Write.Exec("INSERT INTO tasks (title, status, created_ts, updated_ts) VALUES ('Oldest undo', 'open', ?, ?)", now, now)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	seedEffect(t, d, seededEffect{
		Origin: "proposal", Operation: "task_add", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":task_add",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
		Action: `{"op":"task_add"}`, Undo: fmt.Sprintf(`{"op":"task_delete","id":%d}`, taskID), State: "done", CreatedAt: now - 40, LeaseAt: now - 40, Settled: true,
	})
	for i := range 500 {
		seedEffect(t, d, seededEffect{
			Origin: "proposal", Operation: "task_add", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":task_add:" + strconv.Itoa(i),
			Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
			Action: `{"op":"task_add"}`, State: "done", CreatedAt: now, LeaseAt: now, Settled: true,
		})
	}
	ran := runApp(t, "proposals", "undo", strconv.FormatInt(id, 10))
	if ran.code != cli.ExitOK {
		t.Fatalf("proposals undo: %d %s", ran.code, ran.stderr)
	}
	if !strings.Contains(ran.stdout, `"undone": true`) {
		t.Fatalf("undo result = %s, want undone true", ran.stdout)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the oldest undoable effect was outside the read window")
	}
}
