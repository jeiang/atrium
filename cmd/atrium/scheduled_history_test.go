package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

func scheduledDigest(t *testing.T, d *db.DB) string {
	t.Helper()
	var content string
	if err := d.Read.QueryRow("SELECT m.content_json FROM thread_messages m JOIN threads t ON t.id = m.thread_id WHERE t.role = 'today' AND m.content_json LIKE '%\"type\":\"digest\"%' ORDER BY m.id DESC LIMIT 1").Scan(&content); err != nil {
		t.Fatalf("digest message: %v", err)
	}
	return content
}

func scheduledEnded(d *db.DB, name string) int {
	var n int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? AND r.ended_at IS NOT NULL", name).Scan(&n); err != nil {
		return -1
	}
	return n
}

func scheduledOnce(t *testing.T, f *fixture, d *db.DB, name string) {
	t.Helper()
	before := scheduledEnded(d, name)
	schedulerDue(t, d, name)
	schedulerRun(t, f, d, func() bool { return scheduledEnded(d, name) > before })
}

func TestScheduledHistoryKeepsRunThreadsAndTheDigestReportsTheNightlyOutcome(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")

	api.Push(asstReply("weekly review written"))
	scheduledOnce(t, f, d, "weekly-review")
	if status, text := schedulerStatus(t, d, "weekly-review"); status != "ok" {
		t.Fatalf("weekly-review = %s %s, want ok", status, text)
	}
	var weeklyRun, weeklyJob, thread int64
	if err := d.Read.QueryRow("SELECT r.id, r.job_id, r.thread_id FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = 'weekly-review' ORDER BY r.id DESC LIMIT 1").Scan(&weeklyRun, &weeklyJob, &thread); err != nil {
		t.Fatalf("the weekly run kept no thread: %v", err)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM runs WHERE thread_id = ? AND state = 'done'", thread); n == 0 {
		t.Fatalf("the weekly thread %d has no finished run", thread)
	}

	now := float64(time.Now().UnixMicro()) / 1e6
	asstExec(t, d, "UPDATE job_runs SET status = 'running', ended_at = NULL, tokens = 0, cost = 0 WHERE id = ?", weeklyRun)
	asstExec(t, d, "UPDATE jobs SET lease_owner = 'gone', lease_until = ? WHERE id = ?", now+3600, weeklyJob)
	var nightlyJob int64
	if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = 'nightly'").Scan(&nightlyJob); err != nil {
		t.Fatal(err)
	}
	res, err := d.Write.Exec("INSERT INTO job_runs (job_id, due_at, started_at, status) VALUES (?, ?, ?, 'running')", nightlyJob, now-60, now-60)
	if err != nil {
		t.Fatal(err)
	}
	lostRun, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "UPDATE jobs SET lease_owner = 'gone', lease_until = ?, last_run_id = ? WHERE id = ?", now-1, lostRun, nightlyJob)
	schedulerRun(t, f, d, schedulerTerminal(d, "weekly-review", "nightly"))

	var tokens int64
	var weeklyStatus string
	if err := d.Read.QueryRow("SELECT status, tokens FROM job_runs WHERE id = ?", weeklyRun).Scan(&weeklyStatus, &tokens); err != nil {
		t.Fatal(err)
	}
	if weeklyStatus != "ok" || tokens == 0 {
		t.Errorf("the resumed weekly run settled %s with %d tokens, want ok with its usage", weeklyStatus, tokens)
	}
	if status, text := schedulerStatus(t, d, "nightly"); status != "error" || text != "worker lost" {
		t.Errorf("the lost nightly run = %s %q, want error \"worker lost\"", status, text)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM jobs WHERE lease_owner = 'gone'"); n != 0 {
		t.Errorf("%d leases survived settlement", n)
	}

	w := asstBrowse(t, startServeIn(t, dir))
	code, page, _ := w.get("/scheduled", false)
	w.want(code, http.StatusOK, page, fmt.Sprintf("href=\"/chat/%d\"", thread), "worker lost")

	scheduledOnce(t, f, d, "daily-nudges")
	if digest := scheduledDigest(t, d); !strings.Contains(digest, "Dreaming failed") {
		t.Errorf("digest %q does not report the failed nightly run", digest)
	}

	asstSpend(t, d, inference.SourceLoop, 150)
	scheduledOnce(t, f, d, "nightly")
	if status, _ := schedulerStatus(t, d, "nightly"); status != "skipped_budget" {
		t.Fatalf("nightly over budget = %s, want skipped_budget", status)
	}
	scheduledOnce(t, f, d, "daily-nudges")
	if digest := scheduledDigest(t, d); !strings.Contains(digest, "Dreaming skipped: budget") || strings.Contains(digest, "Dreaming failed") {
		t.Errorf("digest %q does not report the budget-skipped nightly run", digest)
	}
}
