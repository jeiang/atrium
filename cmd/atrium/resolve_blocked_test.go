package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail/mailops"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestOperatorResolvesBlockedWritesAndTheFollowUpTreatsThemAsSettled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
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
	for i, account := range []string{"icloud", "gmail"} {
		if _, err := seed.Write.Exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, ?, 'box', 'box', 'filed')", i+1, account); err != nil {
			t.Fatal(err)
		}
	}
	message := func(uid int) int64 {
		return dbtest.AddMessage(t, seed, uid, "body", map[string]any{"subject": "m", "from_addr": "a@shop.test", "date_ts": now})
	}
	const heldText = "cancelled, write outcome unknown: write in flight"
	blockedProposal := func(name, op, text string, ts float64, state string) int64 {
		id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, name, map[string]any{}, map[string]any{}, name, now-5000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'failed', decided_ts = ? WHERE id = ?", now-4000, id); err != nil {
			t.Fatal(err)
		}
		target := "proposal:" + strconv.FormatInt(id, 10)
		direction, action, operation := "forward", `{"op":"`+op+`"}`, op
		var undoOf *int64
		if op == "undo" {
			direction, action, operation = "undo", `{"op":"move_back"}`, "move_back"
			forward := seedEffect(t, seed, seededEffect{
				Origin: "proposal", Operation: "move", OperationKey: target + ":move", Direction: "forward",
				Target: target, ProposalID: &id, Action: `{"op":"move"}`, Undo: `{"op":"move_back"}`,
				State: "done", CreatedAt: ts - 10, LeaseAt: ts - 10, Settled: true,
			})
			undoOf = &forward
		}
		seedEffect(t, seed, seededEffect{
			Origin: "proposal", Operation: operation, OperationKey: target + ":" + op, Direction: direction,
			UndoOf: undoOf, ProposalID: &id, Target: target, Action: action, State: state,
			Error: text, CreatedAt: ts, LeaseAt: ts, Settled: state != "intent",
		})
		return id
	}
	autoAction := func(msg int64, kind, dest string, started float64) int64 {
		key := ledger.AutoKey(msg, kind, dest)
		target := fmt.Sprintf("mail:icloud:%d", msg)
		res, err := seed.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, account, message_id, target, action_json, src, dest, undo_json, state, settled_at) "+
			"VALUES (?, ?, ?, 'mail_auto', ?, ?, 'forward', 'icloud', ?, ?, '{}', 'INBOX', ?, ?, 'done', ?)",
			now-5000, now-5000, fmt.Sprintf("fwd-%d-%s", msg, kind), kind, key, msg, target, dest, `{"op":"move_back"}`, now-5000)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := seed.Write.Exec("INSERT OR IGNORE INTO effect_claims (resource) VALUES (?)", key); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("INSERT INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, 1)", id, key); err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, account, message_id, target, action_json, src, dest, state, error) "+
			"VALUES (?, ?, ?, 'mail_auto', 'move_back', ?, 'undo', ?, 'icloud', ?, ?, '{}', 'INBOX', ?, 'intent', ?)",
			started, started, fmt.Sprintf("undo-%d", id), key, id, msg, target, dest, heldText); err != nil {
			t.Fatal(err)
		}
		return id
	}
	correction := func(msg int64) {
		if _, err := seed.Write.Exec("INSERT INTO corrections (message_id, account, folder, ts) VALUES (?, 'icloud', 'Bills', ?)", msg, now); err != nil {
			t.Fatal(err)
		}
	}
	viaCLI := blockedProposal("cli", "move", heldText, now-4000, "unknown")
	viaWeb := blockedProposal("web", "move", mailops.PartialPrefix+"expunge failed", now-4000, "partial")
	recent := blockedProposal("recent", "move", heldText, now, "intent")
	undoRecentProposal := blockedProposal("undorecent", "undo", heldText, now, "intent")
	undoHeld := blockedProposal("undoheld", "undo", mailops.UnknownPrefix+": connection lost", now-4000, "unknown")
	undoCLI := autoAction(message(1), "file", "Bills", now-4000)
	undoWeb := autoAction(message(2), "file", "Receipts", now-4000)
	undoRecent := autoAction(message(3), "file", "Travel", now)
	correctedMsg, keptMsg := message(4), message(5)
	correctDone := autoAction(correctedMsg, "correct", "Bills", now-4000)
	correctKept := autoAction(keptMsg, "correct", "Bills", now-4000)
	correction(correctedMsg)
	correction(keptMsg)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	if got := runApp(t, "proposals", "approve", id(viaCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "resolve it in the mailbox") {
		t.Fatalf("approve of a blocked proposal = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp(t, "proposals", "resolve", id(recent)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("resolve of a write still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp(t, "proposals", "resolve", id(undoRecentProposal), "--outcome", "not-undone"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("resolve of an undo still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp(t, "live", "resolve", id(undoRecent), "--outcome", "undone"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("live resolve of an undo still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp(t, "proposals", "resolve", id(undoHeld)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "blocked undo") {
		t.Fatalf("resolve of a blocked undo without an outcome = %d %q, want a refusal", got.code, got.stderr)
	}
	for _, verb := range []string{"resolve", "dismiss"} {
		if got := runApp(t, "proposals", verb, "99999"); got.code != cli.ExitError || got.stderr != "error: unknown proposal 99999\n" {
			t.Fatalf("%s of an unknown proposal = %d %q", verb, got.code, got.stderr)
		}
	}
	if got := runApp(t, "proposals", "resolve", id(viaCLI), "--outcome", "applied"); got.code != cli.ExitOK {
		t.Fatalf("proposals resolve = %d %q", got.code, got.stderr)
	}
	if got := runApp(t, "proposals", "resolve", id(viaCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "no blocked write") {
		t.Fatalf("second resolve = %d %q, want a refusal", got.code, got.stderr)
	}
	approved := runApp(t, "proposals", "approve", id(viaCLI))
	if approved.code == cli.ExitOK || !strings.Contains(approved.stderr, "already has a completed write") {
		t.Fatalf("approve after resolve = %d %q %q, want the completed-work refusal", approved.code, approved.stdout, approved.stderr)
	}
	if strings.Contains(approved.stdout+approved.stderr, "resolve it in the mailbox") {
		t.Fatalf("approve after resolve still refused as unresolved: %q", approved.stdout)
	}

	if got := runApp(t, "live", "undo", id(undoCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "resolve it in the mailbox") {
		t.Fatalf("undo of a blocked action = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp(t, "live", "resolve", id(undoCLI)); got.code == cli.ExitOK {
		t.Fatalf("live resolve without an outcome exited %d", got.code)
	}
	if got := runApp(t, "live", "resolve", id(undoCLI), "--outcome", "undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve = %d %q", got.code, got.stderr)
	}
	if got := runApp(t, "live", "undo", id(undoCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "cannot be undone") {
		t.Fatalf("undo of a settled action = %d %q, want it closed", got.code, got.stderr)
	}
	if got := runApp(t, "live", "resolve", id(correctDone), "--outcome", "undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve of a correction = %d %q", got.code, got.stderr)
	}
	if got := runApp(t, "live", "resolve", id(correctKept), "--outcome", "not-undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve not-undone = %d %q", got.code, got.stderr)
	}
	web := localWebG1(t, startServeIn(t, dir))
	blockedWrite := func(proposal string) bool {
		t.Helper()
		pending := webOKG1(t, web.do(http.MethodGet, "/web/v1/pending", nil))
		blocked, _ := pending["blocked"].([]any)
		for _, item := range blocked {
			a := item.(map[string]any)
			if strconv.FormatInt(number(t, a["id"]), 10) == proposal {
				return a["blocked_effect"] == true
			}
		}
		return false
	}
	if !blockedWrite(id(viaWeb)) {
		t.Fatalf("pending does not offer the resolve action on the blocked write")
	}
	if got := runApp(t, "proposals", "resolve", id(undoHeld), "--outcome", "not-undone"); got.code != cli.ExitOK {
		t.Fatalf("proposals resolve of a blocked undo = %d %q", got.code, got.stderr)
	}
	if got := webOKG1(t, web.do(http.MethodGet, "/web/v1/effects/"+id(undoWeb), nil)); got["undo_blocked"] != true {
		t.Fatalf("the auto action does not show its blocked undo: %v", got)
	}
	expectAPI(t, web.do(http.MethodPost, "/web/v1/approvals/"+id(viaWeb)+"/resolve", map[string]any{"outcome": "applied"}, "X-CSRF-Token", ""), http.StatusForbidden, "csrf_failed")
	expectAPI(t, web.do(http.MethodPost, "/web/v1/effects/"+id(undoWeb)+"/resolve", map[string]any{"outcome": "undone"}, "X-CSRF-Token", ""), http.StatusForbidden, "csrf_failed")
	webOKG1(t, web.do(http.MethodPost, "/web/v1/approvals/"+id(viaWeb)+"/resolve", map[string]any{"outcome": "applied"}))
	webOKG1(t, web.do(http.MethodPost, "/web/v1/effects/"+id(undoWeb)+"/resolve", map[string]any{"outcome": "undone"}))
	expectAPI(t, web.do(http.MethodPost, "/web/v1/effects/"+id(undoWeb)+"/resolve", map[string]any{"outcome": "undone"}), http.StatusConflict, "not_resolved")
	if blockedWrite(id(viaWeb)) {
		t.Errorf("settled proposal still offers resolve")
	}

	check, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	rows, err := check.Read.Query("SELECT r.actor, r.source, e.operation, r.outcome, r.prior_error FROM effect_resolutions r " +
		"JOIN effects e ON e.id = r.effect_id WHERE e.proposal_id IS NOT NULL ORDER BY r.id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var actor, source, kind, outcome, prior string
		if err := rows.Scan(&actor, &source, &kind, &outcome, &prior); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{actor, source, kind, outcome}, "/"))
		if prior != "" && !strings.Contains(prior, "unknown") && !strings.Contains(prior, "partial") && !strings.Contains(prior, "copied but not expunged") {
			t.Errorf("ledger kept prior error %q, want the blocking text", prior)
		}
	}
	want := []string{
		"operator/cli/move/applied", "operator/cli/move_back/not-undone", "operator/api/move/applied",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("effect_resolutions = %v, want %v", got, want)
	}
	var approval int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM effect_resolutions "+
		"WHERE effect_id IS NULL AND proposal_id = ? AND outcome = 'approved'", viaCLI).Scan(&approval); err != nil {
		t.Fatal(err)
	}
	if approval != 0 {
		t.Errorf("approval audits = %d for the refused reapproval, want none", approval)
	}
	var undone int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND direction = 'forward' AND undone_by IS NOT NULL").Scan(&undone); err != nil || undone != 3 {
		t.Errorf("auto actions marked undone = %d (%v), want 3", undone, err)
	}
	var kept, cleared int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM corrections WHERE message_id = ?", keptMsg).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("correction of a not-undone action = %d (%v), want it kept", kept, err)
	}
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM corrections WHERE message_id = ?", correctedMsg).Scan(&cleared); err != nil || cleared != 0 {
		t.Errorf("correction of an undone action = %d (%v), want it cleared", cleared, err)
	}
	var stillBlocked int
	if err := check.Read.QueryRow("SELECT (SELECT COUNT(*) FROM effects WHERE undo_of = ? AND state = 'intent') + "+
		"(SELECT COUNT(*) FROM effects WHERE proposal_id = ? AND direction = 'undo' AND state = 'intent')",
		undoRecent, undoRecentProposal).Scan(&stillBlocked); err != nil || stillBlocked != 2 {
		t.Errorf("undos still in flight = %d (%v), want both left untouched", stillBlocked, err)
	}
	if _, err := check.Write.Exec("DELETE FROM effect_resolutions"); err == nil {
		t.Error("effect_resolutions accepted a delete, want append-only")
	}
}

func TestBlockedProposalWithoutErrorTextShowsOutcomeUnknownAndResolves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
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
	pid, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, "silent", map[string]any{}, map[string]any{}, "silent", now-5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("UPDATE proposals SET status = 'failed', decided_ts = ? WHERE id = ?", now-4000, pid); err != nil {
		t.Fatal(err)
	}
	seedEffect(t, seed, seededEffect{
		Origin: "proposal", Operation: "move", OperationKey: "proposal:" + strconv.FormatInt(pid, 10) + ":move",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(pid, 10), ProposalID: &pid,
		Action: `{"op":"move"}`, State: "unknown", CreatedAt: now - 4000, LeaseAt: now - 4000, Settled: true,
	})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(pid, 10)

	web := localWebG1(t, startServeIn(t, dir))
	pending := webOKG1(t, web.do(http.MethodGet, "/web/v1/pending", nil))
	found := false
	for _, item := range pending["blocked"].([]any) {
		a := item.(map[string]any)
		found = found || (number(t, a["id"]) == pid && a["blocked_effect"] == true)
	}
	if !found {
		t.Fatalf("pending lacks the blocked proposal with an unknown outcome: %v", pending)
	}
	if got := webOKG1(t, web.do(http.MethodPost, "/web/v1/approvals/"+id+"/resolve", map[string]any{"outcome": "applied"})); got["approval"] == nil {
		t.Fatalf("resolve of a blocked write without error text: %v", got)
	}

	check, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	var outcome, effective, prior string
	if err := check.Read.QueryRow("SELECT outcome, effective_outcome, prior_error FROM effect_resolutions WHERE proposal_id = ?", pid).
		Scan(&outcome, &effective, &prior); err != nil {
		t.Fatal(err)
	}
	if outcome != proposals.OutcomeApplied || effective != "done" || prior != "" {
		t.Errorf("resolution = %q %q %q, want applied/done with no prior error", outcome, effective, prior)
	}
}
