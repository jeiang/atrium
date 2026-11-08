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
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestOperatorResolvesAForwardInterruptedAction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	ctx := context.Background()
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	action := func(state ledger.State, text string) int64 {
		res, err := seed.Write.Exec("INSERT INTO auto_actions (ts, account, message_id, kind, dest, state, error) VALUES (?, 'icloud', 1, 'file', 'Bills', ?, ?)",
			now-100, state, text)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	interrupted := action(ledger.Interrupted, "interrupted before completion")
	unknown := action(ledger.Unknown, "connection lost")
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	base := startServeIn(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, body, _ := fetch(t, client, "GET", base+"/activity", nil)
	if status != http.StatusOK || !strings.Contains(body, "/activity/mail/"+id(interrupted)+"/resolve") ||
		!strings.Contains(body, ">It happened<") || !strings.Contains(body, ">It did not happen<") {
		t.Fatalf("GET /activity = %d, want the forward resolve controls: %.400s", status, body)
	}
	if got := runApp(t, "live", "resolve", id(interrupted), "--outcome", "undone"); got.code == 0 || !strings.Contains(got.stderr, "applied") {
		t.Fatalf("resolve with an undo outcome = %d %q, want a refusal", got.code, got.stderr)
	}
	if status, _, _ := fetch(t, client, "POST", base+"/activity/mail/"+id(interrupted)+"/resolve", url.Values{"outcome": {"applied"}}); status != http.StatusSeeOther {
		t.Fatalf("resolve applied = %d", status)
	}
	if got := runApp(t, "live", "resolve", id(unknown), "--outcome", "not-applied"); got.code != 0 {
		t.Fatalf("resolve not-applied = %d %q", got.code, got.stderr)
	}
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var applied, failed ledger.State
	var failure *string
	if err := d.Read.QueryRow("SELECT state FROM auto_actions WHERE id = ?", interrupted).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT state, error FROM auto_actions WHERE id = ?", unknown).Scan(&failed, &failure); err != nil {
		t.Fatal(err)
	}
	if applied != ledger.Applied || failed != ledger.Failed || failure == nil || *failure != proposals.NotAppliedText {
		t.Fatalf("resolved states = %q %q (%v), want applied and failed", applied, failed, failure)
	}
}
