package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

func TestTaskAddEffectUndoAndActivityListing(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("t1", "task_add", map[string]any{"title": "Call the dentist", "due": "2026-11-09"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("task_add run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var taskID, effectID int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Call the dentist'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'task_add' AND direction = 'forward'").Scan(&effectID); err != nil {
		t.Fatal(err)
	}

	listed := runApp(t, "effects", "list", "--operation", "task_add")
	if listed.code != cli.ExitOK {
		t.Fatalf("effects list: %d %s", listed.code, listed.stderr)
	}
	var effects []ledger.Effect
	if err := json.Unmarshal([]byte(listed.stdout), &effects); err != nil {
		t.Fatalf("effects list output %q: %v", listed.stdout, err)
	}
	if len(effects) != 1 || effects[0].ID != effectID || effects[0].State != ledger.Done || !effects[0].Undoable {
		t.Fatalf("effects list = %+v, want one done undoable effect %d", effects, effectID)
	}
	if effects[0].Target != "task:"+strconv.FormatInt(taskID, 10) {
		t.Fatalf("effect target = %q, want the task id", effects[0].Target)
	}

	wlist := runApp(t, "writes", "list", "--operation", "task_add")
	if wlist.code != cli.ExitOK {
		t.Fatalf("writes list: %d %s", wlist.code, wlist.stderr)
	}
	var writes []ledger.Effect
	if err := json.Unmarshal([]byte(wlist.stdout), &writes); err != nil {
		t.Fatalf("writes list output %q: %v", wlist.stdout, err)
	}
	if len(writes) != 1 || writes[0].ID != effectID {
		t.Fatalf("writes list = %+v, want effect %d", writes, effectID)
	}

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/activity", nil)
	if code != http.StatusOK || !strings.Contains(body, "/activity/effect/"+strconv.FormatInt(effectID, 10)+"/undo") {
		t.Fatalf("activity page = %d, want one effect undo: %.400s", code, body)
	}

	undone := runApp(t, "writes", "undo", strconv.FormatInt(effectID, 10))
	if undone.code != cli.ExitOK {
		t.Fatalf("writes undo: %d %s", undone.code, undone.stderr)
	}
	var undo ledger.Effect
	if err := json.Unmarshal([]byte(undone.stdout), &undo); err != nil {
		t.Fatalf("effects undo output %q: %v", undone.stdout, err)
	}
	if undo.Direction != "undo" || undo.UndoOf == nil || *undo.UndoOf != effectID || undo.State != ledger.Done {
		t.Fatalf("undo effect = %+v, want a done undo of %d", undo, effectID)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("effects undo did not delete the task")
	}
	var undoneBy int64
	if err := d.Read.QueryRow("SELECT undone_by FROM effects WHERE id = ?", effectID).Scan(&undoneBy); err != nil {
		t.Fatal(err)
	}
	if undoneBy != undo.ID {
		t.Fatalf("forward undone_by = %d, want %d", undoneBy, undo.ID)
	}

	refused := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10))
	if refused.code == cli.ExitOK {
		t.Fatalf("a second undo succeeded: %s", refused.stdout)
	}
	if !strings.Contains(refused.stderr, "already undone") {
		t.Fatalf("second undo refusal = %q", refused.stderr)
	}

	code, body, _ = fetch(t, client, http.MethodGet, base+"/activity", nil)
	if code != http.StatusOK {
		t.Fatalf("activity page after undo = %d", code)
	}
	if strings.Count(body, "id=\"activity-effect-"+strconv.FormatInt(effectID, 10)+"\"") != 1 {
		t.Fatalf("activity page shows the task effect more than once: %.800s", body)
	}
	if !strings.Contains(body, "Undone") {
		t.Fatalf("activity page does not mark the effect undone: %.800s", body)
	}

	after := runApp(t, "effects", "list", "--operation", "task_add")
	if after.code != cli.ExitOK {
		t.Fatalf("effects list after undo: %d %s", after.code, after.stderr)
	}
	var all []ledger.Effect
	if err := json.Unmarshal([]byte(after.stdout), &all); err != nil {
		t.Fatalf("effects list after undo output %q: %v", after.stdout, err)
	}
	if len(all) != 1 || all[0].ID != effectID || all[0].Undoable || all[0].UndoneBy == nil {
		t.Fatalf("forward effect after undo = %+v", all)
	}
}

func TestEffectResolveRecoversAnExpiredIntentAndRefusesAFreshLease(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	now := float64(time.Now().Unix())
	expired, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state) "+
		"VALUES (?, ?, 'expired', 'agent', 'task_add', 'expired', 'forward', 'task:1', '{}', 'intent')", now-1000, now-1000)
	if err != nil {
		t.Fatal(err)
	}
	expiredID, err := expired.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	listed := runApp(t, "effects", "list", "--blocked")
	if listed.code != cli.ExitOK {
		t.Fatalf("effects list: %d %s", listed.code, listed.stderr)
	}
	var blocked []ledger.Effect
	if err := json.Unmarshal([]byte(listed.stdout), &blocked); err != nil {
		t.Fatalf("blocked list output %q: %v", listed.stdout, err)
	}
	if len(blocked) != 1 || blocked[0].ID != expiredID || blocked[0].State != ledger.Unknown || !blocked[0].Blocked {
		t.Fatalf("blocked list = %+v, want the expired intent recovered to unknown", blocked)
	}

	resolved := runApp(t, "effects", "resolve", strconv.FormatInt(expiredID, 10), "--outcome", "not-applied")
	if resolved.code != cli.ExitOK {
		t.Fatalf("effects resolve: %d %s", resolved.code, resolved.stderr)
	}
	var effect ledger.Effect
	if err := json.Unmarshal([]byte(resolved.stdout), &effect); err != nil {
		t.Fatalf("resolve output %q: %v", resolved.stdout, err)
	}
	if effect.State != ledger.Unknown || effect.Resolution == nil ||
		effect.Resolution.Outcome != "not-applied" || effect.Resolution.EffectiveOutcome != "failed" ||
		effect.Resolution.PriorState != "unknown" || effect.Blocked {
		t.Fatalf("resolved effect = %+v", effect)
	}

	again := runApp(t, "effects", "resolve", strconv.FormatInt(expiredID, 10), "--outcome", "applied")
	if again.code == cli.ExitOK {
		t.Fatalf("a second resolution succeeded: %s", again.stdout)
	}
	if !strings.Contains(again.stderr, "already resolved") {
		t.Fatalf("second resolution refusal = %q", again.stderr)
	}

	fresh, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state) "+
		"VALUES (?, ?, 'fresh', 'agent', 'task_add', 'fresh', 'forward', 'task:2', '{}', 'intent')", now, now)
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := fresh.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	refused := runApp(t, "effects", "resolve", strconv.FormatInt(freshID, 10), "--outcome", "applied")
	if refused.code == cli.ExitOK {
		t.Fatalf("a fresh lease was resolved: %s", refused.stdout)
	}
	if !strings.Contains(refused.stderr, "may still be running") {
		t.Fatalf("fresh lease refusal = %q", refused.stderr)
	}
}

func TestFreshDirectIntentBlocksASecondWrite(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	path := filepath.Join(dir, "files", "1", "note.txt")
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state) "+
		"VALUES (?, ?, 'fresh-block', 'agent', 'file_write', 'fresh-block', 'forward', 'note.txt', '{}', 'intent')", now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, 0)", id, "file:"+path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effect_claims (resource) VALUES (?)", "file:"+path); err != nil {
		t.Fatal(err)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "file_write", map[string]any{"name": "note.txt", "content": "blocked"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "write the note"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	var body string
	if err := d.Read.QueryRow("SELECT coalesce(error, '') FROM tool_calls ORDER BY id DESC LIMIT 1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "already claimed") {
		t.Fatalf("the claimed target was not refused: %s", body)
	}
	var done int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'file_write' AND state = 'done'").Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 0 {
		t.Fatalf("%d file writes completed while the target was claimed", done)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the blocked write touched the file: %v", err)
	}
}

type leaseClock struct {
	mu sync.Mutex
	at float64
}

func (c *leaseClock) Now() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *leaseClock) Advance(seconds float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at += seconds
}

func TestRunningExternalWriteRenewsItsLeasePastTheStuckThreshold(t *testing.T) {
	clock := &leaseClock{at: float64(time.Now().Unix())}
	dir, api, _ := moduleFixtureAt(t, flagsExcept("", ""), clock.Now)
	var recovered int64
	var recoveryErr error
	var d *db.DB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/public/v1/trackers" {
			clock.Advance(ledger.StuckSeconds + 60)
			time.Sleep(2500 * time.Millisecond)
			recovered, recoveryErr = ledger.NewStore(d, clock.Now).Recover(context.Background(), clock.Now()-ledger.StuckSeconds)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"tracker":{"trackerId":"trk_1","trackingNumber":"1Z999AA10123456784"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"trackings":[]}}`))
	}))
	t.Cleanup(srv.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, srv.URL+"/public/v1", "good"))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d = mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, " +
		"source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'UPS', '1Z999AA10123456784', 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, 100, 100)"); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("l1", "track_package", map[string]any{"package_id": 1})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("asked")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "track the package"); ran.code != cli.ExitOK {
		t.Fatalf("track run: %d %s", ran.code, ran.stderr)
	}
	var proposal int64
	if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'track_package' AND status = 'pending' ORDER BY id DESC LIMIT 1").Scan(&proposal); err != nil {
		t.Fatalf("no pending track_package proposal: %v", err)
	}
	if ran := runApp(t, "proposals", "approve", strconv.FormatInt(proposal, 10)); ran.code != cli.ExitOK {
		t.Fatalf("approve tracking: %d %s", ran.code, ran.stderr)
	}
	if recoveryErr != nil {
		t.Fatal(recoveryErr)
	}
	if recovered != 0 {
		t.Fatalf("recovery flipped %d running effects to unknown", recovered)
	}
	var state, tracker string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE operation = 'track_package' AND direction = 'forward'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(ledger.Done) {
		t.Fatalf("the write held past the stuck threshold settled as %q, want done", state)
	}
	if err := d.Read.QueryRow("SELECT tracker_id FROM packages WHERE id = 1").Scan(&tracker); err != nil {
		t.Fatal(err)
	}
	if tracker != "trk_1" {
		t.Fatalf("tracker after the long registration = %q, want trk_1", tracker)
	}
}

func TestResolvedUnknownEffectCanBeUndone(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("u1", "task_add", map[string]any{"title": "Return the parcel"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("added")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "add a task"); ran.code != cli.ExitOK {
		t.Fatalf("task_add run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	var taskID, effectID int64
	if err := d.Read.QueryRow("SELECT id FROM tasks WHERE title = 'Return the parcel'").Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'task_add' AND direction = 'forward'").Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE effects SET state = 'unknown', settled_at = lease_at WHERE id = ?", effectID); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(effectID, 10)
	if ran := runApp(t, "effects", "resolve", id, "--outcome", "applied"); ran.code != cli.ExitOK {
		t.Fatalf("resolve applied: %d %s", ran.code, ran.stderr)
	}
	shown := runApp(t, "effects", "show", id)
	if shown.code != cli.ExitOK {
		t.Fatalf("effects show: %d %s", shown.code, shown.stderr)
	}
	var effect ledger.Effect
	if err := json.Unmarshal([]byte(shown.stdout), &effect); err != nil {
		t.Fatalf("effects show output %q: %v", shown.stdout, err)
	}
	if !effect.Undoable || effect.Blocked || effect.EffectiveOutcome != "done" {
		t.Fatalf("resolved-unknown effect = %+v, want done and undoable", effect)
	}
	undone := runApp(t, "effects", "undo", id)
	if undone.code != cli.ExitOK {
		t.Fatalf("undo of a resolved-unknown effect: %d %s", undone.code, undone.stderr)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the undo of the resolved-unknown effect did not delete the task")
	}
	var undoneBy int64
	if err := d.Read.QueryRow("SELECT undone_by FROM effects WHERE id = ?", effectID).Scan(&undoneBy); err != nil {
		t.Fatal(err)
	}
	if undoneBy == 0 {
		t.Fatal("the forward effect is not marked undone")
	}
}

func TestAcceptedPartialEffectReleasesItsClaims(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	path := filepath.Join(dir, "files", "1", "note.txt")
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state, settled_at) "+
		"VALUES (?, ?, 'partial-claim', 'agent', 'file_write', 'partial-claim', 'forward', 'note.txt', '{}', 'partial', ?)", now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effect_resources (effect_id, resource, holding, retain_on_done) VALUES (?, ?, 1, 0)", id, "file:"+path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO effect_claims (resource) VALUES (?)", "file:"+path); err != nil {
		t.Fatal(err)
	}
	write := func(content string) appRun {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p"+content, "file_write", map[string]any{"name": "note.txt", "content": content})))
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		return runApp(t, "agent", "run", "--thread", "1", "--text", "write the note")
	}
	if ran := write("blocked"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	var body string
	if err := d.Read.QueryRow("SELECT coalesce(error, '') FROM tool_calls ORDER BY id DESC LIMIT 1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "already claimed") {
		t.Fatalf("the partially applied claim did not block the write: %s", body)
	}
	if ran := runApp(t, "effects", "resolve", strconv.FormatInt(id, 10), "--outcome", "partial"); ran.code != cli.ExitOK {
		t.Fatalf("resolve partial: %d %s", ran.code, ran.stderr)
	}
	var claims int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effect_claims WHERE resource = ?", "file:"+path).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("accepting the partial effect kept %d claims on the file", claims)
	}
	if ran := write("accepted"); ran.code != cli.ExitOK {
		t.Fatalf("agent run after the resolution: %d %s", ran.code, ran.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the write after accepting the partial effect did not land: %v", err)
	}
	if string(content) != "accepted" {
		t.Fatalf("file content = %q, want the new write", content)
	}
}

func TestEffectsCLIUsesTheProposalUndoers(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	now := float64(time.Now().Unix())
	res, err := d.Write.Exec("INSERT INTO tasks (title, remind_at, created_ts, updated_ts) VALUES ('Call the plumber', ?, ?, ?)", now+3600, now, now)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	seed := func(token, target, undo string) int64 {
		t.Helper()
		res, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, undo_json, state, settled_at) "+
			"VALUES (?, ?, ?, 'local', 'seed', ?, 'forward', ?, '{}', ?, 'done', ?)", now, now, token, token, target, undo, now)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	reminder := seed("seed-reminder", "reminder:"+strconv.FormatInt(taskID, 10), `{"op":"reminder_cancel","id":`+strconv.FormatInt(taskID, 10)+`}`)
	calendar := seed("seed-calendar", "calendar:icloud:event-1", `{"op":"calendar_restore","calendar":"icloud","uid":"event-1","ics":"BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"}`)
	for _, id := range []int64{reminder, calendar} {
		shown := runApp(t, "effects", "show", strconv.FormatInt(id, 10))
		if shown.code != cli.ExitOK {
			t.Fatalf("effects show %d: %d %s", id, shown.code, shown.stderr)
		}
		var effect ledger.Effect
		if err := json.Unmarshal([]byte(shown.stdout), &effect); err != nil {
			t.Fatalf("effects show %d output %q: %v", id, shown.stdout, err)
		}
		if !effect.Undoable {
			t.Fatalf("effect %d is not undoable through the effects CLI", id)
		}
	}
	undone := runApp(t, "effects", "undo", strconv.FormatInt(reminder, 10))
	if undone.code != cli.ExitOK {
		t.Fatalf("undo the reminder: %d %s", undone.code, undone.stderr)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the effects CLI did not cancel the reminder task")
	}
}

func TestOperationKeyAdmissionRefusesASecondSuccessfulAttempt(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, "http://ship24.invalid/public/v1", "dummy"))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("INSERT INTO packages (id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, " +
		"source_message_id, last_message_id, updated_ts, first_seen) VALUES (1, 'UPS', '1Z999AA10123456784', 'Shop', 'shop', '', 'shipped', 'Shipped', 1, 1, 100, 100)"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	const identity = "tracking:ups:1Z999AA10123456784"
	if _, err := d.Write.Exec("INSERT INTO effects (created_at, lease_at, token, origin, operation, operation_key, direction, target, action_json, state, settled_at) "+
		"VALUES (?, ?, 'admission-done', 'tracking', 'track_package', ?, 'forward', ?, '{}', 'done', ?)",
		now, now, "track_package:"+identity, identity, now); err != nil {
		t.Fatal(err)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("a1", "track_package", map[string]any{"package_id": 1})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("asked")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "track it again"); ran.code != cli.ExitOK {
		t.Fatalf("track run: %d %s", ran.code, ran.stderr)
	}
	var proposal int64
	if err := d.Read.QueryRow("SELECT id FROM proposals WHERE kind = 'track_package' AND status = 'pending' ORDER BY id DESC LIMIT 1").Scan(&proposal); err != nil {
		t.Fatalf("no pending track_package proposal: %v", err)
	}
	ran := runApp(t, "proposals", "approve", strconv.FormatInt(proposal, 10))
	if ran.code == cli.ExitOK {
		t.Fatalf("a second attempt with a successful operation key was admitted: %s", ran.stdout)
	}
	if !strings.Contains(ran.stdout+ran.stderr, "already claimed") {
		t.Fatalf("the refusal = %q, want the operation-key conflict", ran.stdout+ran.stderr)
	}
	var writes int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation_key = ? AND direction = 'forward'", "track_package:"+identity).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("%d forward effects for the operation key, want the seeded one only", writes)
	}
}
