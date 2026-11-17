package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/cli"
)

func TestOperatorSchedulesRunOnTimeRunNowAndPushOncePerFailure(t *testing.T) {
	t.Parallel()
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	browser.store(t, d)

	created := runApp(t, "schedule", "create", "Water the plants", "--schedule", "daily 03:00", "--prompt", "Remind me to water the plants")
	if created.code != cli.ExitOK {
		t.Fatalf("schedule create: %d %s", created.code, created.stderr)
	}
	var made struct {
		ID  int64 `json:"id"`
		Job struct {
			Source   string `json:"source"`
			Revision string `json:"revision"`
		} `json:"job"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &made); err != nil || made.ID == 0 || made.Job.Source != "operator" || made.Job.Revision == "" {
		t.Fatalf("schedule create output %q: %v", created.stdout, err)
	}
	job := made.ID
	id := strconv.FormatInt(job, 10)
	if ran := runApp(t, "schedule", "create", "Too often", "--schedule", "*/5 * * * *", "--prompt", "Spin"); ran.code != cli.ExitError {
		t.Fatalf("a schedule below the floor was created: %s", ran.stdout)
	}
	if ran := runApp(t, "schedule", "create", "Broken", "--schedule", "whenever", "--prompt", "Spin"); ran.code != cli.ExitUsage {
		t.Fatalf("invalid schedule exit %d, want usage: %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "schedule", "run", id); ran.code != cli.ExitUnavailable {
		t.Fatalf("run without serve exit %d, want unavailable: %s", ran.code, ran.stderr)
	}

	agentTool := func(name string, args map[string]any) string {
		t.Helper()
		api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", name, args)), asstReply("done"))
		if ran := runApp(t, "agent", "run", "--text", "about my plant schedule", "--kind", "chat"); ran.code != cli.ExitOK {
			t.Fatalf("agent run %s: %d %s", name, ran.code, ran.stderr)
		}
		var thread int64
		if err := d.Read.QueryRow("SELECT MAX(id) FROM threads").Scan(&thread); err != nil {
			t.Fatal(err)
		}
		return asstToolResult(t, d, thread)
	}
	if got := agentTool("schedule_list", map[string]any{}); !strings.Contains(got, "Water the plants") || !strings.Contains(got, "read_only") {
		t.Fatalf("agent list does not show the operator schedule read-only: %s", got)
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"schedule_update", map[string]any{"id": job, "prompt": "Do nothing"}},
		{"schedule_update", map[string]any{"id": job, "enabled": false}},
		{"schedule_cancel", map[string]any{"id": job}},
	} {
		if got := agentTool(call.name, call.args); !strings.Contains(got, "managed by the operator") {
			t.Fatalf("agent %s %v of an operator schedule was not refused: %s", call.name, call.args, got)
		}
	}
	var prompt, jobState string
	if err := d.Read.QueryRow("SELECT prompt, state FROM jobs WHERE id = ?", job).Scan(&prompt, &jobState); err != nil || prompt != "Remind me to water the plants" || jobState != "active" {
		t.Fatalf("operator schedule prompt %q state %q after the agent edits: %v", prompt, jobState, err)
	}

	token := localAPIToken(t, "operator-schedules")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	jobURL := "/api/v1/schedules/" + id
	runStatus := func(run int64) string {
		var status string
		_ = d.Read.QueryRow("SELECT status FROM job_runs WHERE id = ?", run).Scan(&status)
		return status
	}
	runNow := func() int64 {
		t.Helper()
		reply := s.do(http.MethodPost, jobURL+"/run", token, nil)
		if reply.status != http.StatusAccepted || reply.json["status"] != "requested" || number(t, reply.json["job"]) != job {
			t.Fatalf("run now: %d %s", reply.status, reply.body)
		}
		return number(t, reply.json["run"])
	}
	settled := func(run int64, want string) {
		t.Helper()
		waitUntil(t, 60*time.Second, fmt.Sprintf("run %d to end %s", run, want), func() bool { return runStatus(run) == want })
	}
	failures := func() []string {
		t.Helper()
		push.mu.Lock()
		received := append([]pushRequest{}, push.received...)
		push.mu.Unlock()
		var out []string
		for _, r := range received {
			if got := fmt.Sprint(browser.decrypt(t, r.body)); strings.Contains(got, "Failed: Water the plants") {
				out = append(out, got)
			}
		}
		return out
	}
	waitFailures := func(n int) {
		t.Helper()
		waitUntil(t, 15*time.Second, fmt.Sprintf("%d failure pushes", n), func() bool { return len(failures()) >= n })
	}

	api.Push(asstReply("watered"))
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE id = ?", float64(time.Now().Unix())-1, job)
	waitUntil(t, 60*time.Second, "the scheduled run", func() bool {
		return asstCount(t, d, fmt.Sprintf("SELECT COUNT(*) FROM job_runs WHERE job_id = %d AND status = 'ok'", job)) == 1
	})
	listed := s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil)
	row := scheduleByID(t, apiReplyItems(t, listed), job)
	if row == nil || row["source"] != "operator" || row["protected"] != false || row["last_run_at"] == nil || row["next_run_at"] == nil || row["last_run_id"] == nil || row["last_status"] != "ok" {
		t.Fatalf("listed operator schedule: %v", row)
	}

	api.Push(anthropictest.Failure(400, "invalid_request_error", "plants refused"))
	first := runNow()
	settled(first, "error")
	waitFailures(1)
	runRow := s.do(http.MethodGet, fmt.Sprintf("%s/runs/%d", jobURL, first), token, nil)
	row = apiBody(t, runRow, http.StatusOK)
	if number(t, row["id"]) != first || number(t, row["job_id"]) != job || row["status"] != "error" || row["thread_id"] == nil {
		t.Fatalf("run read: %s", runRow.body)
	}
	if got, link := failures()[0], fmt.Sprintf("url:/next/chat/%d]", number(t, row["thread_id"])); !strings.Contains(got, "plants refused") || !strings.Contains(got, link) {
		t.Fatalf("failure push lacks the error or the run link %s: %s", link, got)
	}
	expectAPI(t, s.do(http.MethodGet, fmt.Sprintf("%s/runs/%d", jobURL, first+1000), token, nil), http.StatusNotFound, "not_found")

	api.Push(anthropictest.Failure(400, "invalid_request_error", "plants refused again"))
	settled(runNow(), "error")
	api.Push(asstReply("watered"))
	settled(runNow(), "ok")
	if n := len(failures()); n != 1 {
		t.Fatalf("%d failure pushes after a repeated failure, want 1", n)
	}
	api.Push(anthropictest.Failure(400, "invalid_request_error", "plants refused after a success"))
	settled(runNow(), "error")
	waitFailures(2)

	release := holdNth(t, api, 1)
	api.Push(asstReply("slow water"))
	held := runNow()
	waitUntil(t, 60*time.Second, "the held run", func() bool { return runStatus(held) == "running" })
	busy := s.do(http.MethodPost, jobURL+"/run", token, nil)
	expectAPI(t, busy, http.StatusConflict, "conflict")
	if !strings.Contains(busy.body, fmt.Sprintf("run %d", held)) {
		t.Fatalf("conflict does not name the running run: %s", busy.body)
	}
	release()
	settled(held, "ok")

	expectAPI(t, s.do(http.MethodPost, "/api/v1/schedules/999999/run", token, nil), http.StatusNotFound, "not_found")
	expectAPI(t, s.do(http.MethodPatch, jobURL, token, map[string]any{"prompt": "Water twice"}, "If-Match", `"stale"`), http.StatusPreconditionFailed, "precondition_failed")
	current := scheduleByID(t, apiReplyItems(t, s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil)), job)
	edited := s.do(http.MethodPatch, jobURL, token, map[string]any{"prompt": "Water twice", "name": "Plants"}, "If-Match", `"`+current["revision"].(string)+`"`)
	if edited.status != http.StatusOK || edited.json["job"].(map[string]any)["prompt"] != "Water twice" || edited.json["job"].(map[string]any)["name"] != "Plants" {
		t.Fatalf("edit with the current revision: %d %s", edited.status, edited.body)
	}
	if paused := s.do(http.MethodPatch, jobURL, token, map[string]any{"enabled": false}); paused.status != http.StatusOK || paused.json["enabled"] != false {
		t.Fatalf("disable through patch: %d %s", paused.status, paused.body)
	}
	expectAPI(t, s.do(http.MethodPatch, jobURL, token, map[string]any{"schedule": "*/5 * * * *", "enabled": true}), 409, "refused")

	for _, key := range []string{"mail/sync", backup.JobKey} {
		var builtin int64
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE declaration_key = ?", key).Scan(&builtin); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		url := "/api/v1/schedules/" + strconv.FormatInt(builtin, 10)
		expectAPI(t, s.do(http.MethodPut, url+"/enabled", token, map[string]any{"enabled": false}), 409, "refused")
		expectAPI(t, s.do(http.MethodPatch, url, token, map[string]any{"enabled": false}), 409, "refused")
		expectAPI(t, s.do(http.MethodDelete, url, token, nil), 409, "refused")
		if row := scheduleByID(t, apiReplyItems(t, s.do(http.MethodGet, "/api/v1/schedules?limit=100", token, nil)), builtin); row == nil || row["protected"] != true || row["enabled"] != true || row["source"] != "built-in" {
			t.Fatalf("%s after refused changes: %v", key, row)
		}
	}

	if ran := runApp(t, "schedule", "delete", id); ran.code != cli.ExitOK {
		t.Fatalf("schedule delete: %d %s", ran.code, ran.stderr)
	}
	expectAPI(t, s.do(http.MethodPost, jobURL+"/run", token, nil), http.StatusNotFound, "not_found")
	if n := len(failures()); n != 2 {
		t.Fatalf("%d failure pushes at the end, want 2", n)
	}
}
