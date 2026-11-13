package main

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailrun "github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/passrun"
)

const serveMailFrom = "statements@bank.test"

func (m *mailSafety) correctionPending(t *testing.T) int64 {
	t.Helper()
	uid := m.add("INBOX", "Bank statement", serveMailFrom, "target", "Your statement is ready.")
	m.run(t, "sync")
	id := m.messageID(t, "Bank statement")
	m.run(t, "live", "rebaseline")
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
		id, mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`}, nil)
	return id
}

func mailJobDue(t *testing.T, d *db.DB, key string) *float64 {
	t.Helper()
	var due *float64
	if err := d.Read.QueryRow("SELECT next_due FROM jobs WHERE declaration_key = ?", key).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}

func serveWatching(t *testing.T, dir string, d *db.DB, fakes ...*mailtest.FakeClient) {
	t.Helper()
	before := schedulerRuns(t, d, passrun.Sync)
	started := float64(time.Now().Unix())
	startServeIn(t, dir)
	waitUntil(t, 30*time.Second, "serve to prime the mail sync job", func() bool {
		due := mailJobDue(t, d, mailrun.SyncJobKey)
		return due != nil && *due >= started
	})
	if due := mailJobDue(t, d, mailrun.SyncJobKey); *due < started+115 || *due > float64(time.Now().Unix())+125 {
		t.Fatalf("mail sync next due %.0f s after serve start, want the 2 minute initial delay", *due-started)
	}
	if n := schedulerRuns(t, d, passrun.Sync); n != before {
		t.Fatalf("%d mail sync job runs at serve start, want the %d manual ones", n, before)
	}
	waitWatching(t, fakes...)
}

func waitWatching(t *testing.T, fakes ...*mailtest.FakeClient) {
	t.Helper()
	waitUntil(t, 60*time.Second, "every account watch to finish its first pass and idle", func() bool {
		for _, f := range fakes {
			if f.Idling() == 0 {
				return false
			}
		}
		return true
	})
}

func syncDue(t *testing.T, d *db.DB) int {
	t.Helper()
	manual := schedulerRuns(t, d, passrun.Sync)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Unix())-1, mailrun.SyncJobKey)
	return manual
}

func mailJobRuns(t *testing.T, d *db.DB, name string) []string {
	t.Helper()
	rows, err := d.Read.Query("SELECT r.status || ' ' || COALESCE(r.owner, '') || ' ' || COALESCE(r.error, '') FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? ORDER BY r.id", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func scheduledRunEnded(d *db.DB, name string, after int) func() bool {
	terminal := schedulerTerminal(d, name)
	return func() bool {
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ?", name).Scan(&n); err != nil {
			return false
		}
		return n > after && terminal()
	}
}

type filedOutcome struct {
	stage, action, dest string
	verbs               []string
	state               string
}

func TestServeSyncJobFilesMailOnce(t *testing.T) {
	var baseline, served filedOutcome
	t.Run("cli", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		id := m.correctionPending(t)
		m.run(t, "sync")
		baseline.stage, baseline.action, baseline.dest = m.decision(t, id)
		baseline.verbs = m.verbs()
		baseline.state = m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
	})
	t.Run("serve", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		d := mustConnect(t, m.path, keyFor(t))
		declareJobsAndParkThem(t, d)
		serveWatching(t, m.dir, d, m.fake)
		id := m.correctionPending(t)
		okBefore, _, _ := passRows(t, m.dir, passrun.Sync)
		manual := syncDue(t, d)
		waitUntil(t, 60*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
		if status, text := schedulerStatus(t, d, passrun.Sync); status != "ok" {
			t.Fatalf("mail sync job status %s: %s", status, text)
		}
		time.Sleep(3 * time.Second)
		if n := schedulerRuns(t, d, passrun.Sync); n != manual+1 {
			t.Fatalf("%d mail sync job runs %v, want the %d manual ones and 1 scheduled", n, mailJobRuns(t, d, passrun.Sync), manual)
		}
		if ok, failed, _ := passRows(t, m.dir, passrun.Sync); ok != okBefore+1 || failed != 0 {
			t.Fatalf("sync pass rows ok=%d failed=%d, want %d and 0", ok, failed, okBefore+1)
		}
		served.stage, served.action, served.dest = m.decision(t, id)
		served.verbs = m.verbs()
		served.state = m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'")
		if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
			t.Fatalf("Bills holds %d messages after the scheduled pass, want 1", n)
		}
	})
	if baseline.stage != "correction" || baseline.action != "file" || baseline.dest != "Bills" || baseline.state != "done" ||
		!slices.Equal(baseline.verbs, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("the CLI baseline is %+v, want a correction filed to Bills by COPY, STORE, EXPUNGE", baseline)
	}
	if served.stage != baseline.stage || served.action != baseline.action || served.dest != baseline.dest ||
		served.state != baseline.state || !slices.Equal(served.verbs, baseline.verbs) {
		t.Fatalf("the scheduled pass filed %+v, want the CLI baseline %+v", served, baseline)
	}
}

func TestManualSyncWaitsForTheServeSyncPass(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	serveWatching(t, m.dir, d, m.fake)
	m.correctionPending(t)
	okBefore, _, _ := passRows(t, m.dir, passrun.Sync)
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		if !isClosed(release) {
			close(release)
		}
	})
	manual := syncDue(t, d)
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the scheduled pass never reached its COPY")
	}

	done := make(chan appRun, 1)
	go func() { done <- runApp(t, "sync") }()
	select {
	case ran := <-done:
		t.Fatalf("manual sync finished beside the running scheduled pass: exit %d, stderr %q", ran.code, ran.stderr)
	case <-time.After(2 * time.Second):
	}
	if n := schedulerRuns(t, d, passrun.Sync); n != manual+1 {
		t.Fatalf("%d mail sync job runs while the manual sync waits, want the %d earlier ones and the scheduled one", n, manual)
	}
	close(release)
	var ran appRun
	select {
	case ran = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("manual sync still waiting after the scheduled pass ended")
	}
	if ran.code != 0 {
		t.Fatalf("manual sync after the scheduled pass exited %d: %s", ran.code, ran.stderr)
	}
	waitUntil(t, 30*time.Second, "both mail sync runs to end", scheduledRunEnded(d, passrun.Sync, manual+1))
	if got := m.verbs(); !slices.Equal(got, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("the two passes wrote %v, want one COPY, STORE, EXPUNGE and nothing from the waiting pass", got)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? AND r.status = 'ok'", passrun.Sync); n != manual+2 {
		t.Fatalf("%d ok mail sync job runs %v, want %d", n, mailJobRuns(t, d, passrun.Sync), manual+2)
	}
	if ok, failed, _ := passRows(t, m.dir, passrun.Sync); ok != okBefore+2 || failed != 0 {
		t.Fatalf("sync pass rows ok=%d failed=%d, want %d and 0", ok, failed, okBefore+2)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages, want 1", n)
	}
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func TestScanJobIsPersistentAndMonthly(t *testing.T) {
	m := mailSafetyFixture(t, "shadow")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	asstExec(t, d, "UPDATE jobs SET next_due = ? WHERE declaration_key = ?", float64(time.Now().Add(-40*24*time.Hour).Unix()), scans.JobKey)

	startServeIn(t, m.dir)
	waitUntil(t, 60*time.Second, "the missed scan run at serve start", scheduledRunEnded(d, passrun.Scan, 0))
	if status, text := schedulerStatus(t, d, passrun.Scan); status != "ok" {
		t.Fatalf("scan job status %s: %s (runs %v)", status, text, mailJobRuns(t, d, passrun.Scan))
	}
	time.Sleep(3 * time.Second)
	if n := schedulerRuns(t, d, passrun.Scan); n != 1 {
		t.Fatalf("%d scan job runs after a missed month, want 1", n)
	}
	if ok, failed, _ := passRows(t, m.dir, passrun.Scan); ok != 1 || failed != 0 {
		t.Fatalf("scan pass rows ok=%d failed=%d, want 1 and 0", ok, failed)
	}
	zone, err := config.Zone(config.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(zone)
	want := float64(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, zone).Unix())
	if due := mailJobDue(t, d, scans.JobKey); due == nil || *due != want {
		t.Fatalf("scan job next due %v, want the first of next month at 00:00 operator time (%.0f)", due, want)
	}
}

func TestPostEffectSyncInsideAPassDoesNotDeadlock(t *testing.T) {
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	h, x := mailtest.RawWith("Bank statement", serveMailFrom, "post-effect", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	m := mailEffectsFixture(t, "live", "icloud", fake)
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	m.ruleFor(t, "work", "Archive", serveMailFrom)
	m.run(t, "sync")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal := m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	if proposal == "<nil>" {
		t.Fatal("no pending inbox_backlog proposal")
	}
	serveWatching(t, m.dir, d, fake)
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	fake.SetStatusHook(func(_ string, messages uint32) uint32 {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return messages
	})
	t.Cleanup(func() {
		if !isClosed(release) {
			close(release)
		}
	})
	manual := syncDue(t, d)
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the scheduled pass never reached its mailbox status")
	}

	done := make(chan appRun, 1)
	go func() { done <- runApp(t, "proposals", "approve", proposal) }()
	time.Sleep(time.Second)
	close(release)
	var ran appRun
	select {
	case ran = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the approval and its post-apply sync did not finish within 30 s of the scheduled pass")
	}
	if ran.code != 0 || !strings.Contains(ran.stdout, `"status": "applied"`) || strings.Contains(ran.stderr, "sync failed") {
		t.Fatalf("approval beside the scheduled pass = (%d, %q), want applied with its post-apply sync", ran.code, ran.stderr)
	}
	waitUntil(t, 30*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if status, text := schedulerStatus(t, d, passrun.Sync); status != "ok" {
		t.Fatalf("mail sync job status %s: %s", status, text)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'proposal' AND operation = 'move'"); state != "done" {
		t.Fatalf("the approved move is %s, want done", state)
	}
	if n := len(fake.Boxes["Archive"].Box.Messages); n != 1 {
		t.Fatalf("Archive holds %d messages, want 1", n)
	}
}

func (m *mailSafety) racePending(t *testing.T) (id int64, uid uint32, proposal string) {
	t.Helper()
	uid = m.add("INBOX", "Bank statement", serveMailFrom, "race", "Your statement is ready.", `\Seen`)
	m.rule(t, "Receipts", serveMailFrom)
	m.run(t, "sync")
	id = m.messageID(t, "Bank statement")
	m.run(t, "live", "rebaseline")
	m.run(t, "live", "backlog")
	proposal = m.scalar(t, "SELECT CAST(id AS TEXT) FROM proposals WHERE kind = 'inbox_backlog' AND status = 'pending' ORDER BY id DESC LIMIT 1")
	if proposal == "<nil>" {
		t.Fatal("no pending inbox_backlog proposal")
	}
	return id, uid, proposal
}

func (m *mailSafety) correctToBills(t *testing.T, id int64, uid uint32) {
	t.Helper()
	m.exec(t, "INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Bills', ?, 'operator')",
		id, mail.WallClock()+60)
	m.fake.Boxes["INBOX"].Box.SetFlags(uid, []string{`\Seen`, `\Answered`}, nil)
}

func (m *mailSafety) holdPassAtCopy(t *testing.T, d *db.DB) (manual int, release func()) {
	t.Helper()
	entered, held := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	m.fake.FailWrite = func(w mailtest.FakeWrite) error {
		if w.Verb == "COPY" && armed.CompareAndSwap(true, false) {
			close(entered)
			<-held
		}
		return nil
	}
	release = func() {
		if !isClosed(held) {
			close(held)
		}
	}
	t.Cleanup(release)
	manual = syncDue(t, d)
	select {
	case <-entered:
	case <-time.After(60 * time.Second):
		t.Fatal("the scheduled pass never reached its COPY")
	}
	return manual, release
}

func (m *mailSafety) assertFiledOnceToBills(t *testing.T, d *db.DB, manual int) {
	t.Helper()
	waitUntil(t, 30*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if n := m.copies(); n != 1 {
		t.Fatalf("%d COPY commands %v, want only the pass's one", n, m.verbs())
	}
	for box, want := range map[string]int{"INBOX": 0, "Bills": 1, "Receipts": 0} {
		if n := len(m.fake.Boxes[box].Box.Messages); n != want {
			t.Fatalf("%s holds %d messages, want %d", box, n, want)
		}
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE state IN ('intent', 'unknown')"); n != 0 {
		t.Fatalf("%d effects left intent or unknown, want none", n)
	}
	if state := m.scalar(t, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "done" {
		t.Fatalf("the pass's correction is %s, want done", state)
	}
}

func TestMailWritersWaitForTheRunningPass(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		d := mustConnect(t, m.path, keyFor(t))
		declareJobsAndParkThem(t, d)
		id, uid, proposal := m.racePending(t)
		serveWatching(t, m.dir, d, m.fake)
		m.correctToBills(t, id, uid)
		manual, release := m.holdPassAtCopy(t, d)

		done := make(chan appRun, 1)
		go func() { done <- runApp(t, "proposals", "approve", proposal) }()
		select {
		case ran := <-done:
			t.Fatalf("the approval finished beside the pass held at its COPY: exit %d, stderr %q", ran.code, ran.stderr)
		case <-time.After(time.Second):
		}
		release()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the approval did not finish after the pass released mail-pass")
		}
		m.assertFiledOnceToBills(t, d, manual)
		if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", proposal); status == "approved" {
			t.Fatalf("the proposal is still %s after its approval ended", status)
		}
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'proposal' AND operation = 'move' AND state <> 'skipped'"); n != 0 {
			t.Fatalf("%d proposal moves recorded after the pass filed the message, want none", n)
		}
	})
	t.Run("agent", func(t *testing.T) {
		m := mailSafetyFixture(t, "live")
		t.Setenv("ATRIUM_IMAP_ADDR", "127.0.0.1:1")
		d := mustConnect(t, m.path, keyFor(t))
		declareJobsAndParkThem(t, d)
		id, uid, _ := m.racePending(t)
		if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
			t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
		}
		serveWatching(t, m.dir, d, m.fake)
		m.correctToBills(t, id, uid)
		manual, release := m.holdPassAtCopy(t, d)

		m.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("r1", "mail_move", map[string]any{"account": "icloud", "folder": "Receipts", "message_ids": []any{id}})))
		m.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
		started := time.Now()
		ran := runApp(t, "agent", "run", "--text", "file the bank statement")
		waited := time.Since(started)
		release()
		if ran.code != cli.ExitOK {
			t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
		}
		if waited < db.ShortLockWait {
			t.Fatalf("the agent move returned after %s, want it to wait %s for mail-pass", waited, db.ShortLockWait)
		}
		var body string
		if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = (SELECT MAX(id) FROM threads) AND role = 'user' AND content_json LIKE '%tool_result%' ORDER BY seq DESC LIMIT 1").Scan(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, mail.PassResource) {
			t.Fatalf("the agent move result %q, want a refusal naming %s", body, mail.PassResource)
		}
		m.assertFiledOnceToBills(t, d, manual)
		if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE operation = 'mail_move' AND state NOT IN ('failed', 'skipped')"); n != 0 {
			t.Fatalf("%d agent moves recorded beside the pass, want none", n)
		}
	})
}
