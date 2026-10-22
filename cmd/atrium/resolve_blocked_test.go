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
	blockedProposal := func(name, text string, ts float64) int64 {
		id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, name, py.Dict{}, py.Dict{}, name, now-5000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'failed', decided_ts = ? WHERE id = ?", now-4000, id); err != nil {
			t.Fatal(err)
		}
		if _, err := proposals.RecordAction(ctx, seed.Write, id, py.Dict{{Key: "op", Value: "move"}}, nil, false, &text, ts); err != nil {
			t.Fatal(err)
		}
		return id
	}
	autoAction := func(message int64) int64 {
		res, err := seed.Write.Exec("INSERT INTO auto_actions (ts, account, message_id, kind, undo, ok, undone_error) VALUES (?, 'icloud', ?, 'file', ?, 1, ?)",
			now-5000, message, `{"op":"move_back"}`, proposals.InFlightText)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	viaCLI := blockedProposal("cli", proposals.InFlightText, now-4000)
	viaWeb := blockedProposal("web", mailops.PartialPrefix+"expunge failed", now-4000)
	recent := blockedProposal("recent", proposals.InFlightText, now)
	undoCLI, undoWeb := autoAction(1), autoAction(2)
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

	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, body, _ := fetch(t, client, "GET", base+"/proposals?status=failed", nil); status != http.StatusOK || !strings.Contains(body, "/proposals/"+id(viaWeb)+"/resolve") {
		t.Fatalf("GET /proposals?status=failed = %d, want the resolve button for the blocked proposal: %.400s", status, body)
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
	if status, body, _ := fetch(t, client, "POST", base+"/proposals/"+id(viaWeb)+"/resolve", url.Values{}); status != http.StatusOK || !strings.Contains(body, "Settled") {
		t.Fatalf("POST proposal resolve = %d: %.400s", status, body)
	}
	if status, body, _ := fetch(t, client, "POST", base+"/activity/mail/"+id(undoWeb)+"/resolve", url.Values{"outcome": {"undone"}}); status != http.StatusOK || !strings.Contains(body, "Settled") {
		t.Fatalf("POST activity resolve = %d: %.400s", status, body)
	}
	if status, body, _ := fetch(t, client, "POST", base+"/activity/mail/"+id(undoWeb)+"/resolve", url.Values{"outcome": {"undone"}}); status != http.StatusOK || !strings.Contains(body, "Not settled") {
		t.Errorf("second activity resolve = %d, want a refusal toast: %.400s", status, body)
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
	want := []string{"operator/cli/proposal_action/settled", "operator/cli/auto_undo/undone", "operator/ui/proposal_action/settled", "operator/ui/auto_undo/undone"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("write_resolutions = %v, want %v", got, want)
	}
	var undone int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM auto_actions WHERE undone_ts IS NOT NULL AND undone_error IS NULL").Scan(&undone); err != nil || undone != 2 {
		t.Errorf("auto actions marked undone = %d (%v), want 2", undone, err)
	}
	if _, err := check.Write.Exec("DELETE FROM write_resolutions"); err == nil {
		t.Error("write_resolutions accepted a delete, want append-only")
	}
}
