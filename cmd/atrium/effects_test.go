package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
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

	base, _, stop := startServeStoppable(t, dir)
	defer stop()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, body, _ := fetch(t, client, http.MethodGet, base+"/activity", nil)
	if code != http.StatusOK || !strings.Contains(body, "/activity/effect/"+strconv.FormatInt(effectID, 10)+"/undo") {
		t.Fatalf("activity page = %d, want one effect undo: %.400s", code, body)
	}

	undone := runApp(t, "effects", "undo", strconv.FormatInt(effectID, 10))
	if undone.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %s", undone.code, undone.stderr)
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
