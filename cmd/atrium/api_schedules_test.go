package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func scheduleByID(t *testing.T, items []any, id int64) map[string]any {
	t.Helper()
	for _, item := range items {
		row := item.(map[string]any)
		if number(t, row["id"]) == id {
			return row
		}
	}
	return nil
}

func TestSchedulesUsageAPI(t *testing.T) {
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, model)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	now := float64(time.Now().UnixMicro()) / 1e6
	at := fmt.Sprintf("daily %02d:00", (time.Now().UTC().Hour()+12)%24)
	for i := range 105 {
		asstExec(t, d, "INSERT INTO jobs (name, kind, prompt, schedule, tz, state, next_due, created_by, created_at, module, activated_at) VALUES (?, 'cron', 'Say hi', ?, 'UTC', 'active', NULL, 'operator', ?, 'assistant', ?)",
			fmt.Sprintf("api-job-%03d", i), at, now, now)
	}
	var job, other, builtin int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'api-job-000'").Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'api-job-001'").Scan(&other); err != nil {
		t.Fatal(err)
	}
	for i := range 105 {
		asstExec(t, d, "INSERT INTO job_runs (job_id, due_at, started_at, ended_at, status) VALUES (?, ?, ?, ?, 'ok')", job, now-float64(1000-i), now-float64(1000-i), now-float64(999-i))
	}
	month := asstMonth(t, now)
	asstExec(t, d, "INSERT INTO usage (attempt_key, ts, month, expires_at, state, budget_class, reservation_nusd, accounted_nusd, source, provider, kind, model, effort) "+
		"VALUES ('api-reserved', ?, ?, ?, 'reserved', 'interactive', 5000000000, 0, 'assistant', 'anthropic', 'chat', 'claude-haiku-5-5', 'medium')", now, month, now+3600)
	var thread int64
	if err := d.Read.QueryRow("SELECT id FROM threads WHERE deleted_at IS NULL ORDER BY id LIMIT 1").Scan(&thread); err != nil {
		t.Fatal(err)
	}
	token := localAPIToken(t, "schedules-api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	waitFor(t, func() bool {
		return d.Read.QueryRow("SELECT id FROM jobs WHERE declaration_key IS NOT NULL AND state != 'cancelled' ORDER BY id LIMIT 1").Scan(&builtin) == nil
	})
	jobURL := "/api/v1/schedules/" + strconv.FormatInt(job, 10)

	all := func() []any {
		t.Helper()
		var out []any
		path := "/api/v1/schedules?limit=100"
		for {
			reply := s.do(http.MethodGet, path, token, nil)
			out = append(out, apiReplyItems(t, reply)...)
			next, _ := reply.json["next_cursor"].(string)
			if next == "" {
				return out
			}
			path = "/api/v1/schedules?limit=100&cursor=" + next
		}
	}
	first := s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil)
	page := apiReplyItems(t, first)
	cursor, _ := first.json["next_cursor"].(string)
	if len(page) != 100 || cursor == "" || first.json["tz"] == nil {
		t.Fatalf("first schedule page: %d %q %.300s", len(page), cursor, first.body)
	}
	rest := s.do(http.MethodGet, "/api/v1/schedules?limit=100&cursor="+cursor, token, nil)
	seen := map[int64]bool{}
	for _, item := range append(page, apiReplyItems(t, rest)...) {
		id := number(t, item.(map[string]any)["id"])
		if seen[id] {
			t.Fatalf("schedule %d listed twice", id)
		}
		seen[id] = true
	}
	if !seen[job] || !seen[other] || !seen[builtin] || rest.json["next_cursor"] != nil {
		t.Fatalf("schedule pages miss jobs: %v %.300s", seen, rest.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/subagents?cursor="+cursor, token, nil), 400, "bad_cursor")
	etag := first.header.Get("ETag")
	if again := s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil, "If-None-Match", etag); etag == "" || again.status != 304 {
		t.Fatalf("schedule conditional: %q %d", etag, again.status)
	}

	runs := s.do(http.MethodGet, jobURL+"/runs?limit=100", token, nil)
	runPage := apiReplyItems(t, runs)
	runCursor, _ := runs.json["next_cursor"].(string)
	if len(runPage) != 100 || runCursor == "" {
		t.Fatalf("history page: %d %q", len(runPage), runCursor)
	}
	older := s.do(http.MethodGet, jobURL+"/runs?limit=100&cursor="+runCursor, token, nil)
	if got := apiReplyItems(t, older); len(got) != 5 || number(t, got[0].(map[string]any)["id"]) >= number(t, runPage[99].(map[string]any)["id"]) {
		t.Fatalf("older history: %s", older.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/schedules/"+strconv.FormatInt(other, 10)+"/runs?limit=100&cursor="+runCursor, token, nil), 400, "bad_cursor")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/schedules/999999/runs", token, nil), 404, "not_found")
	if empty := s.do(http.MethodGet, "/api/v1/schedules/"+strconv.FormatInt(other, 10)+"/runs", token, nil); len(apiReplyItems(t, empty)) != 0 {
		t.Fatalf("other job has runs: %s", empty.body)
	}

	moved := fmt.Sprintf("daily %02d:30", (time.Now().UTC().Hour()+12)%24)
	if patched := s.do(http.MethodPatch, jobURL, token, map[string]any{"schedule": moved}); patched.status != 200 || patched.json["enabled"] != true {
		t.Fatalf("patch schedule: %d %s", patched.status, patched.body)
	}
	listed := s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil, "If-None-Match", etag)
	if row := scheduleByID(t, apiReplyItems(t, listed), job); listed.header.Get("ETag") == etag || row == nil || row["schedule"] != moved || row["prompt"] != "Say hi" || row["name"] != "api-job-000" {
		t.Fatalf("patched schedule not shown: %s", listed.body)
	}
	invalid := s.do(http.MethodPatch, jobURL, token, map[string]any{"schedule": " "})
	expectAPI(t, invalid, 422, "invalid")
	if invalid.json["error"].(map[string]any)["fields"] == nil {
		t.Fatalf("missing fields: %s", invalid.body)
	}
	expectAPI(t, s.do(http.MethodPatch, jobURL, token, map[string]any{"prompt": "Do something else"}), 400, "bad_request")
	expectAPI(t, s.do(http.MethodPatch, jobURL, token, map[string]any{}), 422, "invalid")
	expectAPI(t, s.do(http.MethodPut, jobURL+"/enabled", token, map[string]any{}), 422, "invalid")

	for _, enabled := range []bool{false, false, true, true} {
		reply := s.do(http.MethodPut, jobURL+"/enabled", token, map[string]any{"enabled": enabled})
		if reply.status != 200 || reply.json["enabled"] != enabled {
			t.Fatalf("enabled %v: %d %s", enabled, reply.status, reply.body)
		}
		row := scheduleByID(t, all(), job)
		if row["enabled"] != enabled {
			t.Fatalf("schedule enabled = %v, want %v", row["enabled"], enabled)
		}
	}
	var due float64
	var count int
	if err := d.Read.QueryRow("SELECT next_due, (SELECT COUNT(*) FROM job_runs WHERE job_id = jobs.id) FROM jobs WHERE id = ?", job).Scan(&due, &count); err != nil {
		t.Fatal(err)
	}
	if due <= float64(time.Now().Unix())+60 || count != 105 {
		t.Fatalf("enabling ran or scheduled the job now: next_due %v runs %d", due, count)
	}

	builtinURL := "/api/v1/schedules/" + strconv.FormatInt(builtin, 10)
	expectAPI(t, s.do(http.MethodDelete, builtinURL, token, nil), 409, "refused")
	if row := scheduleByID(t, all(), builtin); row == nil {
		t.Fatal("refused cancel removed the built-in schedule")
	}
	if cancelled := s.do(http.MethodDelete, jobURL, token, nil); cancelled.status != 204 {
		t.Fatalf("cancel: %d %s", cancelled.status, cancelled.body)
	}
	if row := scheduleByID(t, all(), job); row != nil {
		t.Fatalf("cancelled schedule still listed: %v", row)
	}
	expectAPI(t, s.do(http.MethodDelete, jobURL, token, nil), 404, "not_found")
	if history := s.do(http.MethodGet, jobURL+"/runs?limit=100", token, nil); len(apiReplyItems(t, history)) != 100 {
		t.Fatalf("cancelled job lost its history: %s", history.body)
	}
	if history := s.do(http.MethodGet, jobURL+"/runs?limit=100&cursor="+runCursor, token, nil); len(apiReplyItems(t, history)) != 5 {
		t.Fatalf("cancelled job lost its older history: %s", history.body)
	}
	otherURL := "/api/v1/schedules/" + strconv.FormatInt(other, 10)
	if cancelled := s.do(http.MethodDelete, otherURL, token, nil); cancelled.status != 204 {
		t.Fatalf("cancel empty job: %d %s", cancelled.status, cancelled.body)
	}
	if history := s.do(http.MethodGet, otherURL+"/runs", token, nil); len(apiReplyItems(t, history)) != 0 {
		t.Fatalf("cancelled empty job history: %s", history.body)
	}
	asstExec(t, d, "DELETE FROM job_runs WHERE job_id = ?", job)
	if history := s.do(http.MethodGet, jobURL+"/runs?limit=100&cursor="+runCursor, token, nil); len(apiReplyItems(t, history)) != 0 {
		t.Fatalf("empty history with a cursor: %s", history.body)
	}
	asstExec(t, d, "DELETE FROM jobs WHERE id = ?", job)
	expectAPI(t, s.do(http.MethodGet, jobURL+"/runs", token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodGet, jobURL+"/runs?limit=100&cursor="+runCursor, token, nil), 404, "not_found")

	usage := s.do(http.MethodGet, "/api/v1/usage", token, nil)
	if usage.status != 200 {
		t.Fatalf("usage: %d %s", usage.status, usage.body)
	}
	budget := usage.json["budget"].(map[string]any)
	summary := usage.json["summary"].(map[string]any)
	if budget["reserved_usd"] != float64(5) || number(t, summary["calls"]) < 1 {
		t.Fatalf("usage misses settled or reserved attempts: %s", usage.body)
	}
	for _, leak := range []string{"api_key", "\"prompt\"", "attempt_key", "api-reserved"} {
		if strings.Contains(usage.body, leak) {
			t.Fatalf("usage exposes %q: %s", leak, usage.body)
		}
	}
	if again := s.do(http.MethodGet, "/api/v1/usage", token, nil, "If-None-Match", usage.header.Get("ETag")); usage.header.Get("ETag") == "" || again.status != 304 {
		t.Fatalf("usage conditional: %d", again.status)
	}

	requests := len(model.Requests())
	estimate := s.do(http.MethodGet, "/api/v1/memory/estimate", token, nil)
	if estimate.status != 200 || number(t, estimate.json["threads"]) < 1 || len(model.Requests()) != requests {
		t.Fatalf("estimate: %d %s, requests %d -> %d", estimate.status, estimate.body, requests, len(model.Requests()))
	}
	threadURL := "/api/v1/memory/threads/" + strconv.FormatInt(thread, 10)
	if tree := s.do(http.MethodGet, threadURL+"/tree", token, nil); tree.status != 200 || tree.json["roots"] == nil {
		t.Fatalf("memory tree: %d %s", tree.status, tree.body)
	}
	if view := s.do(http.MethodGet, threadURL, token, nil); view.status != 200 || view.json["lines"] == nil {
		t.Fatalf("memory view: %d %s", view.status, view.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/memory/threads/999999/tree", token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/memory/threads/999999", token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/memory/work", token, map[string]any{"thread_id": thread, "queued": true}), 422, "invalid")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/memory/work", token, map[string]any{"thread_id": 999999}), 404, "not_found")
	expectAPI(t, s.do(http.MethodPost, "/api/v1/memory/backfill", token, map[string]any{"thread_id": 999999}), 404, "not_found")
	if len(model.Requests()) != requests {
		t.Fatalf("memory refusals called the model: %d -> %d", requests, len(model.Requests()))
	}
}

func TestSubagentAPI(t *testing.T) {
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, model)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	token := localAPIToken(t, "subagent-api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}

	agents := s.do(http.MethodGet, "/api/v1/subagents", token, nil)
	baseline := len(apiReplyItems(t, agents))
	created := s.do(http.MethodPut, "/api/v1/subagents/api-helper", token, map[string]any{"description": "Checks facts.", "prompt": "Check the facts.", "effort": "low", "tools": []string{"web_fetch"}})
	if created.status != 200 || created.json["name"] != "api-helper" || created.json["enabled"] != true || created.json["effort"] != "low" {
		t.Fatalf("subagent set: %d %s", created.status, created.body)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/subagents/api-helper", token, map[string]any{"tools": []string{"no_such_tool"}}), 409, "refused")
	kept := s.do(http.MethodPut, "/api/v1/subagents/api-helper", token, map[string]any{"effort": "high"})
	if tools, _ := kept.json["tools"].([]any); kept.status != 200 || kept.json["effort"] != "high" || len(tools) != 1 || tools[0] != "web_fetch" {
		t.Fatalf("subagent update lost tools: %d %s", kept.status, kept.body)
	}
	listedAgents := s.do(http.MethodGet, "/api/v1/subagents", token, nil, "If-None-Match", agents.header.Get("ETag"))
	if len(apiReplyItems(t, listedAgents)) != baseline+1 {
		t.Fatalf("subagent list: %s", listedAgents.body)
	}
	if disabled := s.do(http.MethodPut, "/api/v1/subagents/api-helper/enabled", token, map[string]any{"enabled": false}); disabled.status != 200 || disabled.json["enabled"] != false {
		t.Fatalf("subagent disable: %d %s", disabled.status, disabled.body)
	}
	expectAPI(t, s.do(http.MethodPut, "/api/v1/subagents/nobody/enabled", token, map[string]any{"enabled": true}), 404, "not_found")
	expectAPI(t, s.do(http.MethodPut, "/api/v1/subagents/api-helper/enabled", token, map[string]any{}), 422, "invalid")
	if deleted := s.do(http.MethodDelete, "/api/v1/subagents/api-helper", token, nil); deleted.status != 204 {
		t.Fatalf("subagent delete: %d %s", deleted.status, deleted.body)
	}
	expectAPI(t, s.do(http.MethodDelete, "/api/v1/subagents/api-helper", token, nil), 404, "not_found")
	checkedBuiltin := false
	for _, item := range apiReplyItems(t, agents) {
		row := item.(map[string]any)
		if row["builtin"] == true {
			checkedBuiltin = true
			expectAPI(t, s.do(http.MethodDelete, "/api/v1/subagents/"+row["name"].(string), token, nil), 409, "refused")
			name := row["name"].(string)
			var beforeDescription, beforePrompt, beforeEffort, beforeTools string
			if err := d.Read.QueryRow("SELECT description, prompt, effort, tools_json FROM subagents WHERE name = ?", name).Scan(&beforeDescription, &beforePrompt, &beforeEffort, &beforeTools); err != nil {
				t.Fatal(err)
			}
			expectAPI(t, s.do(http.MethodPut, "/api/v1/subagents/"+name, token, map[string]any{"description": "Changed", "prompt": "Changed", "effort": "high", "tools": []string{"web_fetch"}}), 409, "refused")
			var description, prompt, effort, tools string
			if err := d.Read.QueryRow("SELECT description, prompt, effort, tools_json FROM subagents WHERE name = ?", name).Scan(&description, &prompt, &effort, &tools); err != nil {
				t.Fatal(err)
			}
			if description != beforeDescription || prompt != beforePrompt || effort != beforeEffort || tools != beforeTools {
				t.Fatal("refused API save changed the built-in subagent")
			}
			break
		}
	}
	if !checkedBuiltin {
		t.Fatal("no built-in subagent was exercised")
	}
}
