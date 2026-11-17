package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/apps/packages/tracking"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/core/coremodule"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
)

func writeOldPauseFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "pause")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlatformPauseHonorsTheOldBinaryPauseFileForMailWrites(t *testing.T) {
	t.Parallel()
	m := mailSafetyFixture(t, "live")
	blocked := m.add("INBOX", "Bank statement", "statements@bank.test", "blocked", "Your statement is ready.")
	held := m.add("INBOX", "Shop receipt", "orders@shop.test", "held", "Thanks for your order.")
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
		m.messageID(t, "Bank statement"), mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(blocked, []string{`\Seen`}, nil)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" {
			return &mail.WriteInterrupted{Command: "UID COPY", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	if ran := runApp(t, "sync"); ran.code == cli.ExitOK {
		t.Fatal("a pass whose only move had an unknown outcome exited 0")
	}
	m.fake.FailWrite = nil
	unknown := m.scalar(t, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", unknown); state != "unknown" {
		t.Fatalf("the dropped move is %s, want unknown", state)
	}

	path := writeOldPauseFile(t, m.dir)
	if got := m.run(t, "live", "status"); !strings.Contains(got.stdout, `"paused": true`) {
		t.Fatalf("live status with the old pause file: %s", got.stdout)
	}
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Receipts', ?, 'operator')",
		m.messageID(t, "Shop receipt"), mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(held, []string{`\Seen`}, nil)
	writes := len(m.fake.Writes)
	m.run(t, "sync")
	if got := m.verbs()[writes:]; len(got) != 0 {
		t.Fatalf("a pass under the old pause file wrote to IMAP: %v", got)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto'"); n != 1 {
		t.Fatalf("%d automatic actions under the old pause file, want only the blocked one", n)
	}

	m.run(t, "live", "resume")
	pauseTrail(t, func(q string) string { return m.scalar(t, q) }, "the CLI resume", "R", "R")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("resume left the pause file: %v", err)
	}
	m.run(t, "sync")
	if got := m.verbs()[writes:]; len(got) != 3 || got[0] != "COPY" || got[1] != "STORE" || got[2] != "EXPUNGE" {
		t.Fatalf("after resume the write log = %v, want one COPY, STORE, EXPUNGE", got)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE id = ?", unknown); state != "unknown" {
		t.Fatalf("resume changed the blocked move to %s", state)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 0 {
		t.Fatalf("resume retried the blocked move: Bills holds %d messages", n)
	}
	writes = len(m.fake.Writes)
	m.run(t, "sync")
	if got := m.verbs()[writes:]; len(got) != 0 {
		t.Fatalf("the next pass wrote again after resume: %v", got)
	}

	m.run(t, "live", "pause")
	pauseTrail(t, func(q string) string { return m.scalar(t, q) }, "the CLI pause", "RP", "RP")
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"since"`) {
		t.Fatalf("the new pause file %q, %v", raw, err)
	}
	if got := m.run(t, "live", "status"); !strings.Contains(got.stdout, `"paused": true`) {
		t.Fatalf("live status after the new pause: %s", got.stdout)
	}
}

func TestPlatformPauseFromAnotherProcessStopsTheRunningServe(t *testing.T) {
	t.Parallel()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := asstDB(t, dir)
	declareJobsAndParkThem(t, d)
	path := writeOldPauseFile(t, dir)
	scalar := func(q string) string {
		t.Helper()
		var v string
		if err := d.Read.QueryRow(q).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	token := localAPIToken(t, "pause")
	base, _, stop := startServeStoppable(t, f.dir)
	s := &apiSite{t: t, base: base}

	state := apiData(t, s.do(http.MethodGet, "/api/v1/automation/pause", token, nil), http.StatusOK)
	if state["paused"] != true || state["by"] != nil || state["since"] == nil {
		t.Fatalf("pause state from the old file: %v", state)
	}
	if shell := strictJSON[coremodule.Shell](t, "/shell", s.do(http.MethodGet, "/api/v1/shell", token, nil)); !shell.Pause.Paused || shell.Pause.Since == nil {
		t.Fatalf("shell pause field: %+v", shell.Pause)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/packages/poll", token, nil), http.StatusConflict, "refused")

	tick := time.Duration(scheduler.TuningAgentSchedulerTickSeconds) * time.Second
	due := func() {
		asstExec(t, d, "UPDATE jobs SET next_due = ?, lease_owner = NULL, lease_until = NULL WHERE declaration_key = ?", unixNow()-1, unit.Mail+"/"+tracking.JobPackageTracking)
		asstExec(t, d, "UPDATE jobs SET next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = ?", unixNow()-1, assistant.NightlyName)
	}
	cliProcess := func(args ...string) {
		t.Helper()
		if out, err := cliExitProcessArgs(t, args).CombinedOutput(); err != nil {
			t.Fatalf("%v in a separate process: %v %s", args, err, out)
		}
	}

	cliProcess("live", "resume")
	if got := apiData(t, s.do(http.MethodGet, "/api/v1/automation/pause", token, nil), http.StatusOK); got["paused"] != false {
		t.Fatalf("pause state after a separate resume: %v", got)
	}
	pauseTrail(t, scalar, "a separate CLI resume", "R", "R")
	cliProcess("live", "pause")
	pauseTrail(t, scalar, "a separate CLI pause", "RP", "RP")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a separate pause wrote no file: %v", err)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/packages/poll", token, nil), http.StatusConflict, "refused")
	due()
	waitUntil(t, tick+15*time.Second, "the running serve to skip the due jobs", schedulerTerminal(d, tracking.JobPackageTracking, assistant.NightlyName))
	for _, name := range []string{tracking.JobPackageTracking, assistant.NightlyName} {
		if status, text := schedulerStatus(t, d, name); status != "skipped_pause" {
			t.Fatalf("%s %s (%s) after a separate pause, want skipped_pause", name, status, text)
		}
	}

	revision := asstCount(t, d, "SELECT value FROM read_revisions WHERE scope = 'automation'")
	resumed := apiData(t, s.do(http.MethodPost, "/api/v1/automation/resume", token, nil), http.StatusOK)
	if resumed["paused"] != false {
		t.Fatalf("platform resume: %v", resumed)
	}
	if got := asstCount(t, d, "SELECT value FROM read_revisions WHERE scope = 'automation'"); got <= revision {
		t.Fatalf("automation topic %d after resume, want above %d", got, revision)
	}
	runs := asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ?", tracking.JobPackageTracking)
	asstExec(t, d, "UPDATE jobs SET next_due = ?, lease_owner = NULL, lease_until = NULL WHERE declaration_key = ?", unixNow()-1, unit.Mail+"/"+tracking.JobPackageTracking)
	waitUntil(t, tick+15*time.Second, "the package job after resume", func() bool {
		return asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ?", tracking.JobPackageTracking) > runs &&
			schedulerTerminal(d, tracking.JobPackageTracking)()
	})
	if status, text := schedulerStatus(t, d, tracking.JobPackageTracking); status == "skipped_pause" {
		t.Fatalf("package job still skipped after resume: %s", text)
	}

	paused := apiData(t, s.do(http.MethodPost, "/api/v1/automation/pause", token, map[string]any{"reason": "deploy"}), http.StatusOK)
	if paused["paused"] != true || paused["reason"] != "deploy" || paused["by"] == nil || paused["source"] != "api" {
		t.Fatalf("platform pause: %v", paused)
	}
	if got := apiData(t, s.do(http.MethodGet, "/api/v1/mail/live", token, nil), http.StatusOK); got["paused"] != true {
		t.Fatalf("mail live status after the platform pause: %v", got)
	}
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/resume", token, nil), http.StatusOK); got["paused"] != false || len(got) != 1 {
		t.Fatalf("mail resume alias: %v", got)
	}
	pauseTrail(t, scalar, "the alias resume", "RPRPR", "RPRPR")
	if got := apiData(t, s.do(http.MethodGet, "/api/v1/automation/pause", token, nil), http.StatusOK); got["paused"] != false {
		t.Fatalf("pause state after the alias resume: %v", got)
	}
	browser := newWebClient(t, s.base, nil)
	browser.session("local")
	apiData(t, browser.do(http.MethodPost, "/web/v1/automation/pause", map[string]any{}), http.StatusOK)
	apiData(t, browser.do(http.MethodPost, "/web/v1/automation/resume", nil), http.StatusOK)
	pauseTrail(t, scalar, "the web prefix pause and resume", "RPRPRPR", "RPRPRPR")

	stop()
	seedSettings(t, map[string]string{module.FlagKey(unit.Mail): "false"})
	base, _, stop = startServeStoppable(t, f.dir)
	s.base = base
	apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/pause", token, nil), http.StatusOK)
	if got := apiData(t, s.do(http.MethodPost, "/api/v1/mail/live/resume", token, nil), http.StatusOK); got["paused"] != false {
		t.Fatalf("mail resume alias with mail off: %v", got)
	}
	pauseTrail(t, scalar, "the aliases with mail off", "RPRPRPRPR", "RPRPRPRPR")

	stop()
	seedSettings(t, map[string]string{module.FlagKey(unit.Notifications): "false"})
	s.base = startServeIn(t, f.dir)
	cliProcess("live", "pause")
	apiData(t, s.do(http.MethodPost, "/api/v1/automation/resume", token, nil), http.StatusOK)
	pauseTrail(t, scalar, "a pause and resume with notifications off", "RPRPRPRPR", "RPRPRPRPRPR")
}

func TestPlatformPauseIsNotAnAgentTool(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	start := len(api.Requests())
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "automation_pause", map[string]any{"reason": "agent"})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("p2", "pause", map[string]any{})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	if ran := runApp(t, "agent", "run", "--text", "pause automation"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()[start:]
	for _, id := range []string{"p1", "p2"} {
		got := findToolResult(t, requests, id)
		if got == nil || got["is_error"] != true || !strings.Contains(fmt.Sprint(got["content"]), "unknown tool") {
			t.Errorf("agent pause call %s returned %v, want an unknown-tool refusal", id, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pause")); !os.IsNotExist(err) {
		t.Fatalf("an agent call paused automation: %v", err)
	}
	if got := runApp(t, "live", "status"); !strings.Contains(got.stdout, `"paused": false`) {
		t.Fatalf("live status after the agent call: %s", got.stdout)
	}
}

const (
	pauseNoticesQuery = "SELECT COALESCE(group_concat(CASE title WHEN 'atrium: automation paused' THEN 'P' ELSE 'R' END, ''), '') FROM (SELECT title FROM notifications WHERE title IN ('atrium: automation paused', 'atrium: automation resumed') ORDER BY id)"
	pauseAuditsQuery  = "SELECT COALESCE(group_concat(CASE operation WHEN 'automation.pause' THEN 'P' WHEN 'automation.resume' THEN 'R' ELSE operation END, ''), '') FROM (SELECT operation FROM operation_audit WHERE operation IN ('automation.pause', 'automation.resume', 'mail.pause', 'mail.resume') ORDER BY id)"
)

func pauseTrail(t *testing.T, scalar func(string) string, step, notices, audits string) {
	t.Helper()
	if got := scalar(pauseNoticesQuery); got != notices {
		t.Errorf("after %s the pause notices are %q, want %q", step, got, notices)
	}
	if got := scalar(pauseAuditsQuery); got != audits {
		t.Errorf("after %s the pause audits are %q, want %q", step, got, audits)
	}
}
