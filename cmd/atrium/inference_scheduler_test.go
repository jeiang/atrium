package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
)

func serveUntil(t *testing.T, f *fixture, what string, finished func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() {
		done <- runAppContext(t, f, ctx, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	}()
	for deadline := time.Now().Add(60 * time.Second); !finished(); time.Sleep(50 * time.Millisecond) {
		select {
		case ran := <-done:
			t.Fatalf("serve exited %d before %s: %s", ran.code, what, ran.stderr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			ran := <-done
			t.Fatalf("serve did not reach %s; exit %d: %s", what, ran.code, ran.stderr)
		}
	}
	cancel()
	if ran := <-done; ran.code != cli.ExitOK {
		t.Fatalf("serve exit %d after %s: %s", ran.code, what, ran.stderr)
	}
}

func schedulerSeed(t *testing.T, f *fixture, d *db.DB) {
	t.Helper()
	serveUntil(t, f, "the seeded jobs", func() bool {
		var n int
		return d.Read.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n) == nil && n >= 12
	})
}

func schedulerRun(t *testing.T, f *fixture, d *db.DB, finished func() bool) {
	t.Helper()
	serveUntil(t, f, "the expected job runs", finished)
}

func schedulerDue(t *testing.T, d *db.DB, names ...string) {
	t.Helper()
	now := float64(time.Now().UnixMicro()) / 1e6
	for _, name := range names {
		asstExec(t, d, "UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = ?", now-1, name)
	}
}

func schedulerTerminal(d *db.DB, names ...string) func() bool {
	return func() bool {
		for _, name := range names {
			var status string
			if err := d.Read.QueryRow("SELECT r.status FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? ORDER BY r.id DESC LIMIT 1", name).Scan(&status); err != nil || status == "running" {
				return false
			}
		}
		return true
	}
}

func schedulerStatus(t *testing.T, d *db.DB, name string) (string, string) {
	t.Helper()
	var status string
	var text *string
	if err := d.Read.QueryRow("SELECT r.status, r.error FROM job_runs r JOIN jobs j ON j.id = r.job_id WHERE j.name = ? ORDER BY r.id DESC LIMIT 1", name).Scan(&status, &text); err != nil {
		t.Fatalf("job run of %s: %v", name, err)
	}
	if text == nil {
		return status, ""
	}
	return status, *text
}

func TestInferenceSchedulerRunsModelFreeJobsWhenTheBudgetIsExhausted(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstSpend(t, d, inference.SourceLoop, 150)
	schedulerDue(t, d, "daily-nudges", "weekly-review")
	before := len(api.Requests())
	schedulerRun(t, f, d, schedulerTerminal(d, "daily-nudges", "weekly-review"))

	if status, _ := schedulerStatus(t, d, "daily-nudges"); status == "skipped_budget" {
		t.Errorf("the model-free digest was skipped for budget")
	}
	if status, _ := schedulerStatus(t, d, "weekly-review"); status != "skipped_budget" {
		t.Errorf("the prompt job status = %q, want skipped_budget", status)
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the exhausted budget sent %d provider requests", got)
	}
}

func TestInferenceSchedulerConditionalJobsRunTheirFreeWork(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "DELETE FROM memory_jobs")
	asstSpend(t, d, inference.SourceLoop, 150)
	schedulerDue(t, d, "memory-work", "calendar-candidates")
	before := len(api.Requests())
	schedulerRun(t, f, d, schedulerTerminal(d, "memory-work", "calendar-candidates"))

	for _, name := range []string{"memory-work", "calendar-candidates"} {
		if status, text := schedulerStatus(t, d, name); status == "skipped_budget" {
			t.Errorf("the empty conditional job %s was skipped for budget: %s", name, text)
		}
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the empty conditional jobs sent %d provider requests", got)
	}
}

func TestInferenceUsagePageShowsReservationsAndCeilings(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	now := float64(time.Now().UnixMicro()) / 1e6
	month := asstMonth(t, now)
	insert := func(key, state string, reservation, accounted int64, expires float64) {
		t.Helper()
		asstExec(t, d, "INSERT INTO usage (attempt_key, ts, month, expires_at, state, budget_class, reservation_nusd, accounted_nusd, source, provider, kind, model, effort) "+
			"VALUES (?, ?, ?, ?, ?, 'interactive', ?, ?, 'assistant', 'anthropic', 'chat', 'claude-haiku-5-5', 'medium')",
			key, now, month, expires, state, reservation, accounted)
	}
	insert("reserved", "reserved", 5_000_000_000, 0, now+3600)
	insert("unknown", "unknown", 3_000_000_000, 0, now+3600)
	insert("expired", "expired", 2_000_000_000, 2_000_000_000, now-3600)

	base, logs := startServeLogged(t, dir)
	status, body, _ := fetch(t, http.DefaultClient, "GET", base+"/settings/usage", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /settings/usage = %d: %.500s\n%s", status, body, logs.String())
	}
	for _, want := range []string{"$5.00 reserved", "$3.00 unknown", "$2.00 expired estimate", "Chat pauses at $142.50 (95%)", "scheduled runs at $120.00 (80%)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the usage page does not show %q", want)
		}
	}
}

func TestInferenceSchedulerDefersConditionalPaidWork(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	schedulerSeed(t, f, d)
	asstExec(t, d, "INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')")
	res, err := d.Write.Exec("INSERT INTO mailboxes (account, name, imap_name, role, uidvalidity, highest_uid) VALUES ('icloud', 'INBOX', 'INBOX', 'inbox', 1544650202, 301)")
	if err != nil {
		t.Fatal(err)
	}
	mailbox, _ := res.LastInsertId()
	res, err = d.Write.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, msgid, digest, subject, from_addr, first_seen) VALUES ('icloud', ?, 1544650202, 301, 'm@x', 'd', 'Dinner on Friday', 'friend@example.com', 0)", mailbox)
	if err != nil {
		t.Fatal(err)
	}
	message, _ := res.LastInsertId()
	asstExec(t, d, "INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (?, 'model flagged an event', 'queued', 1)", message)
	asstSpend(t, d, inference.SourceLoop, 150)
	schedulerDue(t, d, "calendar-candidates")
	before := len(api.Requests())
	schedulerRun(t, f, d, schedulerTerminal(d, "calendar-candidates"))

	status, text := schedulerStatus(t, d, "calendar-candidates")
	if status != "skipped_budget" || !strings.Contains(text, "monthly budget") {
		t.Errorf("the queued conditional job status = %q error %q, want a monthly budget deferral", status, text)
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the deferred conditional job sent %d provider requests", got)
	}
	var queued int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM event_candidates WHERE message_id = ? AND status = 'queued'", message).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("the deferred candidate is no longer queued")
	}
	if got := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = 'calendar.draft-candidates' AND outcome = 'refused' AND source = 'job' AND job_run_id IS NOT NULL"); got != 1 {
		t.Errorf("candidate failure audits = %d, want 1", got)
	}
}
