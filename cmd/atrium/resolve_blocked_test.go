package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func TestOperatorResolvesBlockedWritesAndTheFollowUpTreatsThemAsSettled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	t.Setenv("ATRIUM_DB", path)
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	ctx := context.Background()
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
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
	blockedProposal := func(name, op, text string, ts float64) int64 {
		id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, name, py.Dict{}, py.Dict{}, name, now-5000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'failed', decided_ts = ? WHERE id = ?", now-4000, id); err != nil {
			t.Fatal(err)
		}
		if _, err := proposals.RecordAction(ctx, seed.Write, id, py.Dict{{Key: "op", Value: op}, {Key: "of", Value: int64(1)}}, nil, false, &text, ts); err != nil {
			t.Fatal(err)
		}
		return id
	}
	autoAction := func(msg int64, kind, dest string, started float64) int64 {
		res, err := seed.Write.Exec("INSERT INTO auto_actions (ts, account, message_id, kind, dest, undo, ok, undone_error, undo_started_at) VALUES (?, 'icloud', ?, ?, ?, ?, 1, ?, ?)",
			now-5000, msg, kind, dest, `{"op":"move_back"}`, proposals.InFlightText, started)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	correction := func(msg int64) {
		if _, err := seed.Write.Exec("INSERT INTO corrections (message_id, account, folder, ts) VALUES (?, 'icloud', 'Bills', ?)", msg, now); err != nil {
			t.Fatal(err)
		}
	}
	viaCLI := blockedProposal("cli", "move", proposals.InFlightText, now-4000)
	viaWeb := blockedProposal("web", "move", mailops.PartialPrefix+"expunge failed", now-4000)
	recent := blockedProposal("recent", "move", proposals.InFlightText, now)
	undoRecentProposal := blockedProposal("undorecent", proposals.UndoOp, proposals.InFlightText, now)
	undoHeld := blockedProposal("undoheld", proposals.UndoOp, mailops.UnknownPrefix+": connection lost", now-4000)
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

	if got := runApp("proposals", "approve", id(viaCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "resolve it in the mailbox") {
		t.Fatalf("approve of a blocked proposal = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", id(recent)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("resolve of a write still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", id(undoRecentProposal), "--outcome", "not-undone"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("resolve of an undo still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("live", "resolve", id(undoRecent), "--outcome", "undone"); got.code == cli.ExitOK || !strings.Contains(got.stderr, "may still be running") {
		t.Fatalf("live resolve of an undo still in flight = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", id(undoHeld)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "blocked undo") {
		t.Fatalf("resolve of a blocked undo without an outcome = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", "99999"); got.code == cli.ExitOK {
		t.Fatalf("resolve of an unknown proposal exited %d", got.code)
	}
	if got := runApp("proposals", "resolve", id(viaCLI)); got.code != cli.ExitOK {
		t.Fatalf("proposals resolve = %d %q", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", id(viaCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "no blocked write") {
		t.Fatalf("second resolve = %d %q, want a refusal", got.code, got.stderr)
	}
	approved := runApp("proposals", "approve", id(viaCLI))
	var after struct {
		Status string `json:"status"`
	}
	if approved.code != cli.ExitOK || json.Unmarshal([]byte(approved.stdout), &after) != nil || after.Status != proposals.Failed {
		t.Fatalf("approve after resolve = %d %q %q, want the retry to run and fail on its own", approved.code, approved.stdout, approved.stderr)
	}
	if strings.Contains(approved.stdout+approved.stderr, "resolve it in the mailbox") {
		t.Fatalf("approve after resolve still refused as unresolved: %q", approved.stdout)
	}

	if got := runApp("live", "undo", id(undoCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "resolve it in the mailbox") {
		t.Fatalf("undo of a blocked action = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := runApp("live", "resolve", id(undoCLI)); got.code == cli.ExitOK {
		t.Fatalf("live resolve without an outcome exited %d", got.code)
	}
	if got := runApp("live", "resolve", id(undoCLI), "--outcome", "undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve = %d %q", got.code, got.stderr)
	}
	if got := runApp("live", "undo", id(undoCLI)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "cannot be undone") {
		t.Fatalf("undo of a settled action = %d %q, want it closed", got.code, got.stderr)
	}
	if got := runApp("live", "resolve", id(correctDone), "--outcome", "undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve of a correction = %d %q", got.code, got.stderr)
	}
	if got := runApp("live", "resolve", id(correctKept), "--outcome", "not-undone"); got.code != cli.ExitOK {
		t.Fatalf("live resolve not-undone = %d %q", got.code, got.stderr)
	}
	if got := runApp("proposals", "resolve", id(undoHeld), "--outcome", "not-undone"); got.code != cli.ExitOK {
		t.Fatalf("proposals resolve of a blocked undo = %d %q", got.code, got.stderr)
	}

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/proposals?status=failed", nil); status != http.StatusOK || !strings.Contains(body, "/proposals/"+id(viaWeb)+"/resolve") ||
		strings.Count(body, ">Mark checked<") != 2 || strings.Count(body, ">Messages are back<") != 1 || !strings.Contains(body, `role="group" aria-label="Record what you found in the mailbox"`) {
		t.Fatalf("GET /proposals?status=failed = %d, want Mark checked on the two blocked writes and Messages are back only on the blocked undo: %.400s", status, body)
	}
	if status, body, _ := fetch(t, client, "GET", base+"/activity", nil); status != http.StatusOK || !strings.Contains(body, "/activity/mail/"+id(undoWeb)+"/resolve") {
		t.Fatalf("GET /activity = %d, want the resolve button for the blocked undo: %.400s", status, body)
	}
	bareJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	bare := &http.Client{Jar: bareJar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, bare, "POST", base+"/proposals/"+id(viaWeb)+"/resolve", url.Values{}); status != http.StatusForbidden {
		t.Errorf("POST resolve without a CSRF token = %d, want 403", status)
	}
	if status, _, _ := fetch(t, bare, "POST", base+"/activity/mail/"+id(undoWeb)+"/resolve", url.Values{"outcome": {"undone"}}); status != http.StatusForbidden {
		t.Errorf("POST activity resolve without a CSRF token = %d, want 403", status)
	}
	if status, body, _ := fetch(t, client, "POST", base+"/proposals/"+id(viaWeb)+"/resolve", url.Values{}); status != http.StatusOK || !strings.Contains(body, "Marked as checked") {
		t.Fatalf("POST proposal resolve = %d: %.400s", status, body)
	}
	if status, body, header := fetch(t, client, "POST", base+"/activity/mail/"+id(undoWeb)+"/resolve", url.Values{"outcome": {"undone"}}); status != http.StatusSeeOther || header.Get("Location") != "/activity" {
		t.Fatalf("POST activity resolve = %d %q: %.400s", status, header.Get("Location"), body)
	}
	if status, body, _ := fetch(t, client, "POST", base+"/activity/mail/"+id(undoWeb)+"/resolve", url.Values{"outcome": {"undone"}}); status != http.StatusConflict {
		t.Errorf("second activity resolve = %d, want 409: %.400s", status, body)
	}
	if status, body, _ := fetch(t, client, "GET", base+"/proposals?status=failed", nil); status != http.StatusOK || strings.Contains(body, "/proposals/"+id(viaWeb)+"/resolve") {
		t.Errorf("settled proposal still offers resolve (status %d)", status)
	}

	check, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	rows, err := check.Read.Query("SELECT actor, source, kind, row_id, outcome, prior_error FROM write_resolutions ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var actor, source, kind, outcome, prior string
		var row int64
		if err := rows.Scan(&actor, &source, &kind, &row, &outcome, &prior); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{actor, source, kind, outcome}, "/"))
		if !mailops.Blocks(prior) {
			t.Errorf("ledger kept prior error %q, want the blocking text", prior)
		}
	}
	want := []string{
		"operator/cli/proposal_action/settled", "operator/cli/auto_undo/undone", "operator/cli/auto_undo/undone", "operator/cli/auto_undo/not-undone",
		"operator/cli/proposal_action/not-undone", "operator/ui/proposal_action/settled", "operator/ui/auto_undo/undone",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("write_resolutions = %v, want %v", got, want)
	}
	var undone int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM auto_actions WHERE undone_ts IS NOT NULL AND undone_error IS NULL").Scan(&undone); err != nil || undone != 3 {
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
	if err := check.Read.QueryRow("SELECT (SELECT COUNT(*) FROM auto_actions WHERE id = ? AND undone_error = ?) + (SELECT COUNT(*) FROM proposal_actions WHERE proposal_id = ? AND error = ?)",
		undoRecent, proposals.InFlightText, undoRecentProposal, proposals.InFlightText).Scan(&stillBlocked); err != nil || stillBlocked != 2 {
		t.Errorf("undos still in flight = %d (%v), want both left untouched", stillBlocked, err)
	}
	if _, err := check.Write.Exec("DELETE FROM write_resolutions"); err == nil {
		t.Error("write_resolutions accepted a delete, want append-only")
	}
}
