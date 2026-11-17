package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
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
	seedSettingsAt(t, path, allFlagSettings())
	now := float64(time.Now().UnixNano()) / 1e9
	seq := 0
	action := func(text string) int64 {
		seq++
		res, err := seed.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, account, message_id, target, action_json, dest, state, settled_at, error) "+
			"VALUES (?, ?, ?, 'mail_auto', 'file', ?, 'forward', 'icloud', 1, 'mail:icloud:1', '{}', 'Bills', 'unknown', ?, ?)",
			now-100, now-100, fmt.Sprintf("token-%d", seq), fmt.Sprintf("auto/1/file/Bills/%d", seq), now-100, text)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	interrupted := action("interrupted before completion")
	unknown := action("connection lost")
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	c := webG4(t, startServeIn(t, dir))
	listed := c.do(http.MethodGet, "/web/v1/effects?blocked=true", nil)
	items, _ := listed.json["items"].([]any)
	var blocked bool
	for _, raw := range items {
		if e, _ := raw.(map[string]any); number(t, e["id"]) == interrupted && e["blocked"] == true {
			blocked = true
		}
	}
	if listed.status != http.StatusOK || !blocked {
		t.Fatalf("GET /web/v1/effects = %d, want the interrupted forward action blocked for a decision: %.400s", listed.status, listed.body)
	}
	if got := runApp(t, "live", "resolve", id(interrupted), "--outcome", "undone"); got.code == 0 || !strings.Contains(got.stderr, "applied") {
		t.Fatalf("resolve with an undo outcome = %d %q, want a refusal", got.code, got.stderr)
	}
	if got := c.do(http.MethodPost, "/web/v1/effects/"+id(interrupted)+"/resolve", map[string]any{"outcome": "applied"}); got.status != http.StatusOK {
		t.Fatalf("resolve applied = %d %s", got.status, got.body)
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
	var appliedOutcome, failedOutcome *string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE id = ?", interrupted).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE id = ?", unknown).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT effective_outcome FROM effect_resolutions WHERE effect_id = ?", interrupted).Scan(&appliedOutcome); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT effective_outcome FROM effect_resolutions WHERE effect_id = ?", unknown).Scan(&failedOutcome); err != nil {
		t.Fatal(err)
	}
	if applied != ledger.Unknown || failed != ledger.Unknown ||
		appliedOutcome == nil || *appliedOutcome != "done" || failedOutcome == nil || *failedOutcome != "failed" {
		t.Fatalf("resolved states = %q %q (%v %v), want unknown outcomes with done and failed resolutions",
			applied, failed, appliedOutcome, failedOutcome)
	}
}
