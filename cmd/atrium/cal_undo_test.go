package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings"
)

func TestProposalUndoIsRefusedWhileAnUndoRunsOrAfterAPartialUndo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	ctx := settings.With(context.Background(), testSettings())
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	seedSettingsAt(t, path, allFlagSettings())
	now := float64(time.Now().UnixNano()) / 1e9
	applied := func(name string, undo ledger.State, text *string) int64 {
		id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, name, map[string]any{}, map[string]any{}, name, now-100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.Write.Exec("UPDATE proposals SET status = 'applied', decided_ts = ?, applied_ts = ? WHERE id = ?", now-60, now-50, id); err != nil {
			t.Fatal(err)
		}
		forward := seedEffect(t, seed, seededEffect{
			Origin: "proposal", Operation: "move", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":move",
			Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
			Action: `{"op":"move"}`, Undo: `{"op":"move_back"}`, State: "done", CreatedAt: now - 40, LeaseAt: now - 40, Settled: true,
		})
		errorText := ""
		if text != nil {
			errorText = *text
		}
		seedEffect(t, seed, seededEffect{
			Origin: "proposal", Operation: "move_back", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":move",
			Direction: "undo", UndoOf: &forward, ProposalID: &id, Target: "proposal:" + strconv.FormatInt(id, 10),
			Action: `{"op":"move_back"}`, State: string(undo), Error: errorText, CreatedAt: now, LeaseAt: now,
			Settled: undo != ledger.Intent,
		})
		return id
	}
	partialText := mailops.PartialPrefix + "expunge failed"
	running := applied("running", ledger.Intent, nil)
	partial := applied("partial", ledger.Partial, &partialText)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	if got := runApp(t, "proposals", "undo", id(running)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "undo in progress or unresolved") {
		t.Fatalf("undo while an undo runs = %d %q %q, want a refusal", got.code, got.stdout, got.stderr)
	}
	if got := runApp(t, "proposals", "undo", id(partial)); got.code == cli.ExitOK || !strings.Contains(got.stderr, "undo in progress or unresolved") {
		t.Fatalf("undo after a partial undo = %d %q %q, want a refusal", got.code, got.stdout, got.stderr)
	}

	check := mustConnect(t, path)
	defer func() { _ = check.Close() }()
	for _, pid := range []int64{running, partial} {
		var undos int
		var status string
		var undone *float64
		if err := check.Read.QueryRow("SELECT (SELECT COUNT(*) FROM effects e WHERE e.proposal_id = p.id AND e.direction = 'undo'), status, undone_ts FROM proposals p WHERE id = ?", pid).
			Scan(&undos, &status, &undone); err != nil {
			t.Fatal(err)
		}
		if undos != 1 || status != proposals.Applied || undone != nil {
			t.Errorf("proposal %d after a refused undo: %d undo rows, status %q, undone %v; want it untouched", pid, undos, status, undone)
		}
	}
}

type seededEffect struct {
	Origin       string
	Operation    string
	OperationKey string
	Direction    string
	Target       string
	Action       string
	Undo         string
	State        string
	Error        string
	ProposalID   *int64
	UndoOf       *int64
	CreatedAt    float64
	LeaseAt      float64
	Settled      bool
}

var seededTokens int

func seedEffect(t *testing.T, d *db.DB, e seededEffect) int64 {
	t.Helper()
	action := e.Action
	if action == "" {
		action = "{}"
	}
	var undo any
	if e.Undo != "" {
		undo = e.Undo
	}
	var settled any
	if e.Settled {
		settled = e.LeaseAt
	}
	var errorText any
	if e.Error != "" {
		errorText = e.Error
	}
	seededTokens++
	res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, undo_of, "+
		"proposal_id, target, action_json, undo_json, state, settled_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.CreatedAt, e.LeaseAt, fmt.Sprintf("seed-%d-%d", time.Now().UnixNano(), seededTokens), e.Origin, e.Operation,
		e.OperationKey, e.Direction, e.UndoOf, e.ProposalID, e.Target, action, undo, e.State, settled, errorText)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCalendarDeleteUndoRecreatesTheEventAndRecordsIt(t *testing.T) {
	dir, srv := davFixture(t)
	created := proposalID(t, toolResult(t, "calendar_create", map[string]any{
		"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00",
	}))
	if got := runApp(t, "proposals", "approve", created); got.code != cli.ExitOK {
		t.Fatalf("approve create: %d %q %q", got.code, got.stdout, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var uid string
	if err := d.Read.QueryRow("SELECT uid FROM agent_events WHERE deleted_ts IS NULL").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	removed := proposalID(t, toolResult(t, "calendar_delete", map[string]any{"uid": uid}))
	if got := runApp(t, "proposals", "approve", removed); got.code != cli.ExitOK {
		t.Fatalf("approve delete: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if len(srv.Objects()) != 0 {
		t.Fatalf("the delete left %d events", len(srv.Objects()))
	}
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'calendar_delete' AND direction = 'forward'").Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10)); got.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	objects := srv.Objects()
	if len(objects) != 1 {
		t.Fatalf("%d events after the undo, want 1: %v", len(objects), objects)
	}
	var restored string
	if err := d.Read.QueryRow("SELECT uid FROM agent_events WHERE deleted_ts IS NULL").Scan(&restored); err != nil {
		t.Fatalf("the recreated event was not tracked: %v", err)
	}
	if restored == uid {
		t.Fatalf("the recreated event reused the deleted uid %q", uid)
	}
	if _, ok := objects[caldavtest.CalendarPath(0)+restored+".ics"]; !ok {
		t.Fatalf("the fake holds %v, not the recreated event %q", objects, restored)
	}
	var undoState string
	var undoneBy *int64
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE direction = 'undo' AND undo_of = ?", effectID).Scan(&undoState); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT undone_by FROM effects WHERE id = ?", effectID).Scan(&undoneBy); err != nil {
		t.Fatal(err)
	}
	if undoState != string(ledger.Done) || undoneBy == nil {
		t.Fatalf("undo state %q, forward undone_by %v; want a done undo that marks the delete undone", undoState, undoneBy)
	}
	if got := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10)); got.code == cli.ExitOK {
		t.Fatalf("a second undo exited 0: %q", got.stdout)
	}
	if len(srv.Objects()) != 1 {
		t.Fatalf("the refused second undo changed the events: %v", srv.Objects())
	}
}

func TestCalendarCreateUndoKeepsTheEventTrackedWhenTheDeleteFails(t *testing.T) {
	dir, srv := davFixture(t)
	created := proposalID(t, toolResult(t, "calendar_create", map[string]any{
		"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00",
	}))
	if got := runApp(t, "proposals", "approve", created); got.code != cli.ExitOK {
		t.Fatalf("approve create: %d %q %q", got.code, got.stdout, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var uid string
	if err := d.Read.QueryRow("SELECT uid FROM agent_events WHERE deleted_ts IS NULL").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	var effectID int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'calendar_event' AND direction = 'forward'").Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	srv.FailDeletes = 1
	if got := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10)); got.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if len(srv.Objects()) != 1 {
		t.Fatalf("the failed delete removed the event: %v", srv.Objects())
	}
	var deleted *float64
	if err := d.Read.QueryRow("SELECT deleted_ts FROM agent_events WHERE uid = ?", uid).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("the failed delete marked the surviving event deleted at %v", *deleted)
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE direction = 'undo' AND undo_of = ?", effectID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(ledger.Unknown) {
		t.Fatalf("undo state = %q, want unknown", state)
	}
}

func TestProposalsResolveRefusesAMixedDirectionOutcomeWithoutCommitting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	setEnv(t, "ATRIUM_DB", path)
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	ctx := settings.With(context.Background(), testSettings())
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, seed, path); err != nil {
		t.Fatal(err)
	}
	seedSettingsAt(t, path, allFlagSettings())
	now := float64(time.Now().UnixNano()) / 1e9
	id, _, err := proposals.Create(ctx, seed, "deletion_rule", nil, "mixed", map[string]any{}, map[string]any{}, "mixed", now-100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("UPDATE proposals SET status = 'applied', decided_ts = ?, applied_ts = ? WHERE id = ?", now-60, now-50, id); err != nil {
		t.Fatal(err)
	}
	forward := seedEffect(t, seed, seededEffect{
		Origin: "proposal", Operation: "move", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":move",
		Direction: "forward", Target: "proposal:" + strconv.FormatInt(id, 10), ProposalID: &id,
		Action: `{"op":"move"}`, Undo: `{"op":"move_back"}`, State: "unknown", CreatedAt: now - 40, LeaseAt: now - 40, Settled: true,
	})
	seedEffect(t, seed, seededEffect{
		Origin: "proposal", Operation: "move_back", OperationKey: "proposal:" + strconv.FormatInt(id, 10) + ":move",
		Direction: "undo", UndoOf: &forward, ProposalID: &id, Target: "proposal:" + strconv.FormatInt(id, 10),
		Action: `{"op":"move_back"}`, State: "unknown", CreatedAt: now, LeaseAt: now, Settled: true,
	})
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	for _, outcome := range []string{"applied", "undone"} {
		got := runApp(t, "proposals", "resolve", strconv.FormatInt(id, 10), "--outcome", outcome)
		if got.code == cli.ExitOK {
			t.Fatalf("resolve --outcome %s exited 0: %q", outcome, got.stdout)
		}
		if !strings.Contains(got.stderr, "does not apply") {
			t.Fatalf("resolve --outcome %s = %q, want a direction refusal", outcome, got.stderr)
		}
	}
	check := mustConnect(t, path)
	defer func() { _ = check.Close() }()
	var states int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE proposal_id = ? AND state = 'unknown'", id).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 2 {
		t.Fatalf("%d effects are still unknown, want 2: a refused resolve committed part of the proposal", states)
	}
	var resolutions int
	if err := check.Read.QueryRow("SELECT COUNT(*) FROM effect_resolutions WHERE effect_id IN (SELECT id FROM effects WHERE proposal_id = ?)", id).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if resolutions != 0 {
		t.Fatalf("%d resolutions were recorded by a refused resolve", resolutions)
	}
}
