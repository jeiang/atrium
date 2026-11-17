package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestServeRecoversOnlyAnIdleApprovedProposalAndKeepsItsSettledBatchesUndoable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	ctx := testProposalContext()
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	seedSettingsAt(t, path, allFlagSettings())
	now := float64(time.Now().UnixNano()) / 1e9
	for _, rule := range []string{"stale", "recent", "norow", "norowrecent"} {
		if _, err := seed.Write.Exec("INSERT INTO rules (position, id, action, dest, conditions) VALUES (?, ?, 'file', 'Bills', '[]')", len(rule), rule); err != nil {
			t.Fatal(err)
		}
	}
	approve := func(fingerprint, rule string, settledAt float64, held bool) int64 {
		id, _, err := proposals.Create(ctx, seed, "inbox_backlog", nil, fingerprint, map[string]any{}, map[string]any{}, fingerprint, now-1000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'approved', decided_ts = ? WHERE id = ?", settledAt-1, id); err != nil {
			t.Fatal(err)
		}
		undo := `{"op":"rule_delete","id":"` + rule + `"}`
		seedEffect(t, seed, seededEffect{
			Origin: "proposal", Operation: "rule_delete", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":rule_delete:" + rule,
			Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
			Action: undo, Undo: undo, State: "done", CreatedAt: settledAt, LeaseAt: settledAt, Settled: true,
		})
		if held {
			seedEffect(t, seed, seededEffect{
				Origin: "proposal", Operation: "move", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":move",
				Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
				Action: `{"op":"move"}`, State: "intent", CreatedAt: now - 700, LeaseAt: now - 700,
			})
		}
		return id
	}
	stale := approve("stale", "stale", now-800, true)
	recent := approve("recent", "recent", now-5, true)
	noRow := approve("norow", "norow", now-4000, false)
	noRowRecent := approve("norowrecent", "norowrecent", now-1000, false)
	noLedger, _, err := proposals.Create(ctx, seed, "inbox_backlog", nil, "noledger", map[string]any{}, map[string]any{}, "noledger", now-5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("UPDATE proposals SET status = 'approved', decided_ts = ? WHERE id = ?", now-4000, noLedger); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	base := startServeIn(t, dir)

	check, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	status := func(id int64) string {
		var s string
		if err := check.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	ruleExists := func(id string) bool {
		var n int
		if err := check.Read.QueryRow("SELECT COUNT(*) FROM rules WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if got := status(stale); got != proposals.Failed {
		t.Errorf("proposal idle past the bound = %q, want failed", got)
	}
	if got := status(recent); got != proposals.Approved {
		t.Errorf("proposal with a recent settled ledger row = %q, want approved", got)
	}

	if got := status(noRow); got != proposals.Failed {
		t.Errorf("approved proposal with no in-flight row, idle past the unheld bound = %q, want failed", got)
	}
	if got := status(noLedger); got != proposals.Failed {
		t.Errorf("approved proposal with no ledger rows past the unheld bound = %q, want failed", got)
	}
	if got := status(noRowRecent); got != proposals.Approved {
		t.Errorf("approved proposal with no in-flight row inside the unheld bound = %q, want approved", got)
	}

	web := localWebG1(t, base)
	approval := func(id int64, verb string) apiReply {
		return web.do(http.MethodPost, "/web/v1/approvals/"+strconv.FormatInt(id, 10)+"/"+verb, map[string]any{})
	}
	undo := func(id int64) apiReply {
		return web.do(http.MethodPost, "/web/v1/undo/proposal:"+strconv.FormatInt(id, 10), nil)
	}
	if got := approval(stale, "approve"); got.status != http.StatusConflict || !strings.Contains(got.body, "not_approved") || !strings.Contains(got.body, "unresolved write") {
		t.Errorf("approve of the recovered proposal = %d: %.400s", got.status, got.body)
	}
	if got := status(stale); got != proposals.Failed {
		t.Errorf("recovered proposal after the refused approve = %q, want failed", got)
	}
	if got := undo(stale); got.status != http.StatusOK || got.json["undone"] != true {
		t.Errorf("undo of the recovered proposal = %d: %.400s", got.status, got.body)
	}
	if ruleExists("stale") {
		t.Error("undo did not replay the settled batch of the recovered proposal")
	}
	if !ruleExists("recent") {
		t.Error("undo touched a proposal that was not recovered")
	}
	if got := undo(noRow); got.status != http.StatusOK || got.json["undone"] != true {
		t.Errorf("undo of the proposal recovered with no write in flight = %d: %.400s", got.status, got.body)
	}
	if ruleExists("norow") {
		t.Error("undo did not replay the settled batch of the proposal recovered with no write in flight")
	}
	if !ruleExists("norowrecent") {
		t.Error("undo touched a proposal that was not recovered")
	}
	if got := approval(noLedger, "deny"); got.status != http.StatusOK || got.json["approval"].(map[string]any)["status"] != "dismissed" {
		t.Errorf("dismiss of the proposal recovered with no ledger rows = %d: %.400s", got.status, got.body)
	}
	if got := status(noLedger); got != proposals.Dismissed {
		t.Errorf("dismissed no-ledger proposal = %q, want dismissed", got)
	}
	if got := undo(recent); got.status != http.StatusConflict || !strings.Contains(got.body, "not_undone") {
		t.Errorf("undo of a proposal still approved = %d: %.400s", got.status, got.body)
	}
	if !ruleExists("recent") {
		t.Error("a refused undo replayed a proposal that is still approved")
	}
}
