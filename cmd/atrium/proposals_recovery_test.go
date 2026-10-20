package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func TestServeRecoversOnlyAnIdleApprovedProposalAndKeepsItsSettledBatchesUndoable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	ctx := context.Background()
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	for _, rule := range []string{"stale", "recent", "norow", "norowrecent"} {
		if _, err := seed.Write.Exec("INSERT INTO rules (position, id, action, dest, conditions) VALUES (?, ?, 'file', 'Bills', '[]')", len(rule), rule); err != nil {
			t.Fatal(err)
		}
	}
	approve := func(fingerprint, rule string, settledAt float64, held bool) int64 {
		id, _, err := proposals.Create(ctx, seed, "rebalance_split", nil, fingerprint, py.Dict{}, py.Dict{}, fingerprint, now-1000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'approved', decided_ts = ? WHERE id = ?", settledAt-1, id); err != nil {
			t.Fatal(err)
		}
		undo := py.Dict{{Key: "op", Value: "rule_delete"}, {Key: "id", Value: rule}}
		if _, err := proposals.RecordAction(ctx, seed.Write, id, undo, undo, true, nil, settledAt); err != nil {
			t.Fatal(err)
		}
		if held {
			if _, err := proposals.BeginAction(ctx, seed.Write, id, py.Dict{{Key: "op", Value: "move"}}, now-700); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	stale := approve("stale", "stale", now-800, true)
	recent := approve("recent", "recent", now-5, true)
	noRow := approve("norow", "norow", now-4000, false)
	noRowRecent := approve("norowrecent", "norowrecent", now-1000, false)
	noLedger, _, err := proposals.Create(ctx, seed, "rebalance_split", nil, "noledger", py.Dict{}, py.Dict{}, "noledger", now-5000)
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

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, _, _ := fetch(t, client, "GET", base+"/proposals", nil); code != http.StatusOK {
		t.Fatalf("GET /proposals = %d", code)
	}
	route := func(id int64, verb string) string {
		return base + "/proposals/" + strconv.FormatInt(id, 10) + "/" + verb
	}
	code, body, _ := fetch(t, client, "POST", route(stale, "approve"), url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "Not approved") || !strings.Contains(body, "unknown or partial outcome") {
		t.Errorf("approve of the recovered proposal = %d: %.400s", code, body)
	}
	if got := status(stale); got != proposals.Failed {
		t.Errorf("recovered proposal after the refused approve = %q, want failed", got)
	}
	code, body, _ = fetch(t, client, "POST", route(stale, "undo"), url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "Undone") {
		t.Errorf("undo of the recovered proposal = %d: %.400s", code, body)
	}
	if ruleExists("stale") {
		t.Error("undo did not replay the settled batch of the recovered proposal")
	}
	if !ruleExists("recent") {
		t.Error("undo touched a proposal that was not recovered")
	}
	code, body, _ = fetch(t, client, "POST", route(noRow, "undo"), url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "Undone") {
		t.Errorf("undo of the proposal recovered with no write in flight = %d: %.400s", code, body)
	}
	if ruleExists("norow") {
		t.Error("undo did not replay the settled batch of the proposal recovered with no write in flight")
	}
	if !ruleExists("norowrecent") {
		t.Error("undo touched a proposal that was not recovered")
	}
	code, body, _ = fetch(t, client, "POST", route(noLedger, "dismiss"), url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "Dismissed") {
		t.Errorf("dismiss of the proposal recovered with no ledger rows = %d: %.400s", code, body)
	}
	if got := status(noLedger); got != proposals.Dismissed {
		t.Errorf("dismissed no-ledger proposal = %q, want dismissed", got)
	}
	code, body, _ = fetch(t, client, "POST", route(recent, "undo"), url.Values{})
	if code != http.StatusOK || !strings.Contains(body, "Not undone") {
		t.Errorf("undo of a proposal still approved = %d: %.400s", code, body)
	}
}
