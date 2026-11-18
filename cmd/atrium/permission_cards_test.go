package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func pendingPermissions(t *testing.T, d *db.DB, run int64) []int64 {
	t.Helper()
	rows, err := d.Read.Query("SELECT id FROM proposals WHERE kind = 'permission' AND status = 'pending' AND json_extract(payload, '$.run_id') = ? ORDER BY id", run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func lastRun(t *testing.T, d *db.DB, thread int64) (int64, string) {
	t.Helper()
	var id int64
	var state string
	if err := d.Read.QueryRow("SELECT id, state FROM runs WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).Scan(&id, &state); err != nil {
		t.Fatal(err)
	}
	return id, state
}

func waitPermissions(t *testing.T, d *db.DB, thread int64, n int) (int64, []int64) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; {
		run, state := lastRun(t, d, thread)
		if ids := pendingPermissions(t, d, run); state == "parked" && len(ids) == n {
			return run, ids
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d is %q with %v pending permission requests, want parked with %d", run, state, pendingPermissions(t, d, run), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func kbBody(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	var body string
	if err := d.Read.QueryRow("SELECT body FROM kb_records WHERE id = ?", id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func guardedChange(t *testing.T, client *webClient, proposal int64) map[string]any {
	t.Helper()
	card := webOKG2(t, client.do(http.MethodGet, fmt.Sprintf("/web/v1/proposals/%d", proposal), nil))
	payload, _ := fieldG2(card, "payload").(map[string]any)
	change, _ := payload["change"].(map[string]any)
	if payload["kind"] != "permission" || change["kind"] != "guarded_change" {
		t.Fatalf("proposal %d payload = %v, want a permission request with a guarded change", proposal, payload)
	}
	return change
}

func TestOperatorRecordEditAsksWithBeforeAndAfter(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	record := knowledgeCreateG5(t, client, map[string]any{"title": "Water shutoff", "body": "Under the stairs.", "scope": "personal"})

	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("k1", "kb_write", map[string]any{"id": record, "body": "Behind the boiler."})),
		anthropictest.Reply("end_turn", anthropictest.Text("updated")),
	)
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "the shutoff moved behind the boiler"}))
	run, ids := waitPermissions(t, d, thread, 1)
	change := guardedChange(t, client, ids[0])
	fields, _ := change["fields"].([]any)
	body, _ := fields[0].(map[string]any)
	if change["subject"] != "Water shutoff" || !strings.Contains(fmt.Sprint(change["rule"]), "operator-owned") || len(fields) != 1 ||
		body["name"] != "Body" || body["before"] != "Under the stairs." || body["after"] != "Behind the boiler." {
		t.Fatalf("change = %v, want the body before and after", change)
	}
	transcript := webOKG2(t, client.do(http.MethodGet, fmt.Sprintf("/web/v1/threads/%d", thread), nil))
	if asks, _ := fieldG2(transcript, "asks").([]any); len(asks) != 1 || fmt.Sprint(asks[0].(map[string]any)["proposal_id"]) != strconv.FormatInt(ids[0], 10) {
		t.Fatalf("transcript asks = %v, want the parked request", fieldG2(transcript, "asks"))
	}
	if got := kbBody(t, d, record); got != "Under the stairs." {
		t.Fatalf("body before the decision = %q", got)
	}
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/approve", ids[0]), nil))
	waitRunState(t, d, thread, "done")
	if got := kbBody(t, d, record); got != "Behind the boiler." {
		t.Fatalf("body after approval = %q", got)
	}
	var writes int
	var effect int64
	if err := d.Read.QueryRow("SELECT COUNT(*), MAX(id) FROM effects WHERE operation = 'kb_write' AND run_id = ?", run).Scan(&writes, &effect); err != nil || writes != 1 {
		t.Fatalf("kb writes in the run = %d, %v, want one", writes, err)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	if result := toolResultFor(t, requests[1], "k1"); result["is_error"] == true {
		t.Fatalf("approved write returned %v", result)
	}
	if got := runApp(t, "effects", "undo", strconv.FormatInt(effect, 10)); got.code != cli.ExitOK {
		t.Fatalf("undo the approved write: %d %s", got.code, got.stderr)
	}
	if got := kbBody(t, d, record); got != "Under the stairs." {
		t.Fatalf("body after undo = %q", got)
	}

	start = len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "kb_retract", map[string]any{"id": record, "reason": "moved"})),
		anthropictest.Reply("end_turn", anthropictest.Text("left it")),
	)
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "forget the shutoff"}))
	_, ids = waitPermissions(t, d, thread, 1)
	change = guardedChange(t, client, ids[0])
	if change["note"] != "moved" || !strings.HasPrefix(fmt.Sprint(change["title"]), "Retract K") {
		t.Fatalf("retract change = %v", change)
	}
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/deny", ids[0]), nil))
	waitRunState(t, d, thread, "done")
	var retracted bool
	if err := d.Read.QueryRow("SELECT retracted_at IS NOT NULL FROM kb_records WHERE id = ?", record).Scan(&retracted); err != nil || retracted {
		t.Fatalf("record retracted after a denial: %v %v", retracted, err)
	}
	requests = api.Requests()[start:]
	if refusal := toolResultFor(t, requests[len(requests)-1], "r1"); refusal["is_error"] != true || !strings.Contains(fmt.Sprint(refusal["content"]), "the operator denied this request") {
		t.Fatalf("denied retract returned %v", refusal)
	}
}

func TestThreeCardsInOneStepResumeOnce(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	thread := latestThread(t, dir)
	base, _, _ := startServeApp(t, freshApp(f), &lockedBuffer{})
	client := webLocalG2(t, base)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	var records []int64
	for _, title := range []string{"Gate code", "Bin day", "Boiler service"} {
		records = append(records, knowledgeCreateG5(t, client, map[string]any{"title": title, "body": "old", "scope": "personal"}))
	}
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use",
			anthropictest.ToolUse("e1", "kb_write", map[string]any{"id": records[0], "body": "new 1"}),
			anthropictest.ToolUse("e2", "kb_write", map[string]any{"id": records[1], "body": "new 2"}),
			anthropictest.ToolUse("e3", "kb_write", map[string]any{"id": records[2], "body": "new 3"})),
		anthropictest.Reply("end_turn", anthropictest.Text("all updated")),
	)
	webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/threads/%d/messages", thread), map[string]any{"text": "update all three"}))
	run, ids := waitPermissions(t, d, thread, 3)
	transcript := webOKG2(t, client.do(http.MethodGet, fmt.Sprintf("/web/v1/threads/%d", thread), nil))
	if asks, _ := fieldG2(transcript, "asks").([]any); len(asks) != 3 {
		t.Fatalf("transcript asks = %v, want three", fieldG2(transcript, "asks"))
	}
	for i, id := range ids {
		webOKG2(t, client.do(http.MethodPost, fmt.Sprintf("/web/v1/approvals/%d/approve", id), nil))
		if i == len(ids)-1 {
			break
		}
		if _, state := lastRun(t, d, thread); state != "parked" {
			t.Fatalf("run is %q after %d of 3 approvals, want parked", state, i+1)
		}
		if got := len(api.Requests()) - start; got != 1 {
			t.Fatalf("%d model requests after %d of 3 approvals, want 1", got, i+1)
		}
	}
	waitRunState(t, d, thread, "done")
	if latest, _ := lastRun(t, d, thread); latest != run {
		t.Fatalf("approvals started run %d, want the parked run %d to finish", latest, run)
	}
	requests := api.Requests()[start:]
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want one before and one after the approvals", len(requests))
	}
	for i, id := range []string{"e1", "e2", "e3"} {
		if result := toolResultFor(t, requests[1], id); result["is_error"] == true {
			t.Errorf("%s returned %v", id, result)
		}
		if got := kbBody(t, d, records[i]); got != fmt.Sprintf("new %d", i+1) {
			t.Errorf("record %d body = %q", records[i], got)
		}
	}
	var writes int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'kb_write' AND run_id = ?", run).Scan(&writes); err != nil || writes != 3 {
		t.Errorf("kb writes = %d, %v, want three", writes, err)
	}
}
