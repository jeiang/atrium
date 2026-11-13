//go:build !modernc

package main

import (
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/passrun"
)

func TestWebCorrectionWaitsForTheServeSyncPass(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	base, _, _ := startServeWith(t, m.dir, false)
	waitWatching(t, m.fake)
	id := m.correctionPending(t)
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

	client := mrvClient(t)
	if code, _, _ := mrvDo(t, client, "GET", base+"/triage", nil); code != 200 {
		t.Fatalf("GET /triage = %d", code)
	}
	code, body, _ := mrvDo(t, client, "POST", base+"/messages/"+m.scalar(t, "SELECT CAST(id AS TEXT) FROM messages WHERE id = ?", id)+"/correct",
		url.Values{"dest": {"Bills"}, "context": {"row"}}, "HX-Request", "true")
	if code != 200 || !strings.Contains(body, "It could not be moved yet") {
		t.Fatalf("the web correction during the pass = %d: %.400s, want it told the message could not be moved yet", code, body)
	}
	if n := m.copies(); n != 0 {
		t.Fatalf("%d COPY commands completed while the pass held its COPY, want none from the web correction", n)
	}
	close(release)
	waitUntil(t, 60*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if n := m.copies(); n != 1 {
		t.Fatalf("%d COPY commands for the corrected message, want exactly one", n)
	}
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages, want the corrected message once", n)
	}
	if n := len(m.fake.Boxes["INBOX"].Box.Messages); n != 0 {
		t.Fatalf("INBOX still holds %d messages after the correction", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE state = 'unknown'"); n != 0 {
		t.Fatalf("%d unknown effects after the correction", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct' AND state = 'done'"); n != 1 {
		t.Fatalf("%d done correction effects, want 1", n)
	}
}

func TestManualSyncSettlesARunLeftRunningWhileServeWasDown(t *testing.T) {
	m := mailSafetyFixture(t, "shadow")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	for _, pass := range []string{passrun.Sync, passrun.Scan} {
		jobID := m.scalar(t, "SELECT CAST(id AS TEXT) FROM jobs WHERE declaration_key = ?", "mail/"+pass)
		stale := float64(time.Now().Add(-time.Hour).Unix())
		m.exec(t, "INSERT INTO job_runs (job_id, due_at, started_at, heartbeat_at, owner, status) VALUES (?, ?, ?, ?, 'gone:1:dead', 'running')", jobID, stale, stale, stale)
		m.exec(t, "UPDATE jobs SET lease_owner = 'gone:1:dead', lease_until = 0, last_run_id = (SELECT MAX(id) FROM job_runs WHERE job_id = ?) WHERE id = ?", jobID, jobID)
	}

	for _, argv := range [][]string{{"sync"}, {"proposals", "scan-all"}} {
		ran := m.run(t, argv...)
		if ran.code != cli.ExitOK {
			t.Fatalf("%s beside a stale running row = (%d, %q), want success", strings.Join(argv, " "), ran.code, ran.stderr)
		}
	}
	for _, pass := range []string{passrun.Sync, passrun.Scan} {
		key := "mail/" + pass
		if n := m.count(t, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? AND r.status = 'running'", key); n != 0 {
			t.Fatalf("%s still has %d running rows", key, n)
		}
		if lost := m.scalar(t, "SELECT r.status || ':' || COALESCE(r.error, '') FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? AND r.owner = 'gone:1:dead'", key); lost != "error:worker lost" {
			t.Fatalf("%s stale run settled as %s, want error:worker lost", key, lost)
		}
		if ok := m.count(t, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? AND r.status = 'ok' AND r.owner <> 'gone:1:dead'", key); ok != 1 {
			t.Fatalf("%s recorded %d successful manual runs, want 1", key, ok)
		}
	}
}

func TestManualSyncLeavesAnotherJobsStaleRunAlone(t *testing.T) {
	m := mailSafetyFixture(t, "shadow")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	for _, pass := range []string{passrun.Sync, passrun.Scan} {
		jobID := m.scalar(t, "SELECT CAST(id AS TEXT) FROM jobs WHERE declaration_key = ?", "mail/"+pass)
		stale := float64(time.Now().Add(-time.Hour).Unix())
		m.exec(t, "INSERT INTO job_runs (job_id, due_at, started_at, heartbeat_at, owner, status) VALUES (?, ?, ?, ?, 'gone:1:dead', 'running')", jobID, stale, stale, stale)
		m.exec(t, "UPDATE jobs SET lease_owner = 'gone:1:dead', lease_until = 0, last_run_id = (SELECT MAX(id) FROM job_runs WHERE job_id = ?) WHERE id = ?", jobID, jobID)
	}

	m.run(t, "sync")
	if lost := m.scalar(t, "SELECT r.status || ':' || COALESCE(r.error, '') FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = 'mail/"+passrun.Sync+"' AND r.owner = 'gone:1:dead'"); lost != "error:worker lost" {
		t.Fatalf("the sync job's stale run settled as %s, want error:worker lost", lost)
	}
	scan := "mail/" + passrun.Scan
	if row := m.scalar(t, "SELECT r.status || ':' || COALESCE(r.error, '') FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ? AND r.owner = 'gone:1:dead'", scan); row != "running:" {
		t.Fatalf("the scan job's stale run is %s after a manual sync, want it left running", row)
	}
	if lease := m.scalar(t, "SELECT COALESCE(lease_owner, '') FROM jobs WHERE declaration_key = ?", scan); lease != "gone:1:dead" {
		t.Fatalf("the scan job's lease owner is %q after a manual sync, want it untouched", lease)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.declaration_key = ?", scan); n != 1 {
		t.Fatalf("the scan job has %d runs after a manual sync, want only the stale one", n)
	}
}
