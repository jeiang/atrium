package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func operationAudits(t *testing.T, d *db.DB, operation string) []string {
	t.Helper()
	rows, err := d.Read.Query("SELECT outcome || ' ' || target || ' ' || actor || ' ' || source FROM operation_audit WHERE operation = ? ORDER BY id", operation)
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var s string
		err := r.Scan(&s)
		return s, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStandingInstructionsPage(t *testing.T) {
	const carried = "Answer in metric units. Keep replies under five sentences."
	const edited = "Answer in metric units. Name the source of every figure."
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Write.Exec("INSERT INTO settings (key, value) VALUES ('prompt_system', ?), ('prompt_subagent', 'Old child rules.')", carried); err != nil {
		t.Fatal(err)
	}
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}

	page := apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/instructions", token, nil), http.StatusOK)
	rec := page["record"].(map[string]any)
	if rec["body"] != carried || rec["pinned"] != true || rec["confirmed"] != true || rec["title"] != "Standing instructions" {
		t.Fatalf("instructions record: %v", rec)
	}
	id := number(t, rec["id"])
	if n := countRows(t, d, "SELECT COUNT(*) FROM kb_records WHERE origin = 'atrium:instructions'"); n != 1 {
		t.Fatalf("%d instructions records, want 1", n)
	}
	if n := countRows(t, d, "SELECT COUNT(*) FROM settings_audit WHERE key = 'prompt_system' AND source = 'migration'"); n != 1 {
		t.Fatalf("%d carry audit rows, want 1", n)
	}
	if n := countRows(t, d, "SELECT COUNT(*) FROM settings WHERE key IN ('prompt_system', 'prompt_subagent')"); n != 2 {
		t.Fatalf("stored prompt rows = %d, want both kept", n)
	}

	path := fmt.Sprintf("/api/v1/knowledge/%d", id)
	apiData(t, s.do(http.MethodPatch, path, token, map[string]any{"body": edited}), http.StatusOK)

	warm(t, api)
	start := len(api.Requests())
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("noted")))
	if ran := runApp(t, "agent", "run", "--text", "how far is the station"); ran.code != cli.ExitOK {
		t.Fatalf("run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	if len(requests) != 1 {
		t.Fatalf("%d model requests, want 1", len(requests))
	}
	system := systemText(t, requests[0])
	if !strings.Contains(system, "Standing instructions from the operator:\n"+edited) || strings.Count(system, edited) != 1 || strings.Contains(system, carried) {
		t.Fatalf("system prompt does not carry the edited instructions once: %q", system)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "kb_search", map[string]any{"query": "metric units"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("none")))
	if ran := runApp(t, "agent", "run", "--text", "which units do I use"); ran.code != cli.ExitOK {
		t.Fatalf("kb_search run: %d %s", ran.code, ran.stderr)
	}
	if found := kbResults(t, d, latestThread(t, dir), "s1"); len(found) != 0 {
		t.Fatalf("kb_search returned the standing instructions already in the prompt: %v", found)
	}
	all := apiData(t, s.do(http.MethodGet, "/api/v1/search/all?q=metric&types=record", token, nil), http.StatusOK)
	var shown []int64
	for _, g := range all["groups"].([]any) {
		for _, item := range g.(map[string]any)["items"].([]any) {
			shown = append(shown, number(t, item.(map[string]any)["id"]))
		}
	}
	if len(shown) != 1 || shown[0] != id {
		t.Fatalf("operator search does not show the standing instructions: %v", all)
	}

	page = apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/instructions", token, nil), http.StatusOK)
	history := page["history"].([]any)
	if len(history) == 0 {
		t.Fatalf("no history: %v", page)
	}
	last := history[0].(map[string]any)
	if last["by"] != "operator" || last["previous_body"] != carried {
		t.Fatalf("history row: %v", last)
	}
	undone := s.do(http.MethodPost, fmt.Sprintf("/api/v1/effects/%d/undo", number(t, last["id"])), token, nil)
	if undone.status != http.StatusOK || undone.json["undone"] != true {
		t.Fatalf("undo: %d %s", undone.status, undone.body)
	}
	page = apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/instructions", token, nil), http.StatusOK)
	if body := page["record"].(map[string]any)["body"]; body != carried {
		t.Fatalf("body after undo = %v, want the previous text", body)
	}

	expectAPI(t, s.do(http.MethodDelete, path, token, nil), http.StatusConflict, "refused")
	if n := countRows(t, d, "SELECT COUNT(*) FROM kb_records WHERE origin = 'atrium:instructions'"); n != 1 {
		t.Fatalf("purge removed the instructions: %d rows", n)
	}

	profile := apiData(t, s.do(http.MethodGet, "/api/v1/knowledge/profile", token, nil), http.StatusOK)
	if strings.Contains(fmt.Sprint(profile), carried) {
		t.Errorf("the profile lists the instructions: %v", profile)
	}
}

func TestRoleCRUDIsGone(t *testing.T) {
	s, token, _ := localAPISite(t)
	if got := s.do(http.MethodGet, "/api/v1/subagents", token, nil); got.status != http.StatusNotFound {
		t.Fatalf("GET /api/v1/subagents = %d %s, want 404", got.status, got.body)
	}
	if got := s.do(http.MethodPut, "/api/v1/subagents/mail/enabled", token, map[string]any{"enabled": false}); got.status != http.StatusNotFound {
		t.Fatalf("PUT /api/v1/subagents/mail/enabled = %d, want 404", got.status)
	}
	if ran := runApp(t, "agent", "subagents", "list"); ran.code == cli.ExitOK {
		t.Fatalf("agent subagents list still runs: %s", ran.stdout)
	}
}
