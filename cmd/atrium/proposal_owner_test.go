package main

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
)

func disabledMailFixture(t *testing.T) (*db.DB, float64) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	if err := db.Migrate(testProposalContext(), seed, path); err != nil {
		t.Fatal(err)
	}
	kv := allFlagSettings()
	kv[module.FlagKey(module.Mail)] = "false"
	seedSettingsAt(t, path, kv)
	return seed, float64(time.Now().UnixNano()) / 1e9
}

func TestProposalDismissalWorksWhileItsOwnerIsDisabled(t *testing.T) {
	seed, now := disabledMailFixture(t)
	id, _, err := proposals.Create(testProposalContext(), seed, "mail_triage", nil, "dismiss me",
		map[string]any{}, map[string]any{}, "dismiss-me", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "proposals", "dismiss", strconv.FormatInt(id, 10)); got.code != cli.ExitOK {
		t.Fatalf("dismiss while mail is disabled = %d %q %q, want success", got.code, got.stdout, got.stderr)
	}
	var status string
	if err := seed.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != proposals.Dismissed {
		t.Fatalf("status after dismissal = %s, want %s", status, proposals.Dismissed)
	}
}

func TestOperatorResolveOfAMailWriteWaitsForTheOwnerToBeEnabled(t *testing.T) {
	seed, now := disabledMailFixture(t)
	ctx := testProposalContext()
	id, _, err := proposals.Create(ctx, seed, "mail_triage", nil, "blocked",
		map[string]any{}, map[string]any{}, "blocked", now-5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("UPDATE proposals SET status = 'failed', decided_ts = ? WHERE id = ?", now-4000, id); err != nil {
		t.Fatal(err)
	}
	target := "proposal:" + strconv.FormatInt(id, 10)
	effect := seedEffect(t, seed, seededEffect{
		Origin: "proposal", Operation: "move", OperationKey: target + ":move", Direction: "forward",
		Target: target, ProposalID: &id, Action: `{"op":"move"}`, State: "unknown", Error: "connection lost",
		CreatedAt: now - 4000, LeaseAt: now - 4000, Settled: true,
	})
	idArg := strconv.FormatInt(id, 10)
	if got := runApp(t, "proposals", "resolve", idArg, "--outcome", "applied"); got.code == cli.ExitOK {
		t.Fatalf("resolve while mail is disabled exited 0: %q", got.stdout)
	}
	var state string
	if err := seed.Read.QueryRow("SELECT state FROM effects WHERE id = ?", effect).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("refused resolve changed the effect to %s, want unknown", state)
	}
	var resolutions int
	if err := seed.Read.QueryRow("SELECT COUNT(*) FROM effect_resolutions WHERE effect_id = ?", effect).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if resolutions != 0 {
		t.Fatalf("refused resolve recorded %d resolutions, want 0", resolutions)
	}
	asstSet(t, module.FlagKey(module.Mail), "true")
	if got := runApp(t, "proposals", "resolve", idArg, "--outcome", "applied"); got.code != cli.ExitOK {
		t.Fatalf("resolve after re-enabling mail = %d %q %q, want success", got.code, got.stdout, got.stderr)
	}
}
