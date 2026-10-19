package openloops_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

// The clocks of make_db.py's runs: the open-loop refresh at t0, the reminder's approval a day later.
const (
	proposeClock = 1_792_080_000.0
	approveClock = 1_792_166_400.0
)

type proposalRow struct{ kind, title, payload, preview, fingerprint string }

func goldenReminder(t *testing.T, d *db.DB) (int64, proposalRow) {
	t.Helper()
	var id int64
	var r proposalRow
	err := d.Read.QueryRow("SELECT id, kind, title, payload, preview, fingerprint FROM proposals WHERE kind = 'reminder'").
		Scan(&id, &r.kind, &r.title, &r.payload, &r.preview, &r.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return id, r
}

func TestProposeRebuildsTheGoldenReminder(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	id, want := goldenReminder(t, d)
	exec(t, d, "UPDATE proposals SET fingerprint = 'previous' WHERE id = ?", id)
	exec(t, d, "UPDATE commitments SET proposal_id = NULL")
	r := &openloops.Reminders{DB: d, Now: atClock(proposeClock), Env: env()}
	n, err := r.Propose(ctx, proposeClock, tz)
	if err != nil || n != 1 {
		t.Fatalf("Propose = %d, %v", n, err)
	}
	var got proposalRow
	var created float64
	err = d.Read.QueryRow("SELECT kind, title, payload, preview, fingerprint, created_ts FROM proposals WHERE fingerprint != 'previous' AND kind = 'reminder'").
		Scan(&got.kind, &got.title, &got.payload, &got.preview, &got.fingerprint, &created)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || created != proposeClock {
		t.Errorf("proposal = %+v at %v\nwant %+v", got, created, want)
	}
}

func atClock(ts float64) func() float64 { return func() float64 { return ts } }

func TestExecuteReproducesTheGoldenJobAndActions(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	id, _ := goldenReminder(t, d)
	exec(t, d, "INSERT INTO proposals (kind, account, status, created_ts, title, payload, preview, fingerprint) "+
		"SELECT kind, account, 'pending', created_ts, title, payload, preview, 'clone' FROM proposals WHERE id = ?", id)
	var clone int64
	if err := d.Read.QueryRow("SELECT id FROM proposals WHERE fingerprint = 'clone'").Scan(&clone); err != nil {
		t.Fatal(err)
	}
	reg := proposals.NewRegistry()
	openloops.Register(reg, &openloops.Reminders{DB: d, Now: atClock(approveClock), Env: env()})
	done, err := (&proposals.Pipeline{DB: d, Registry: reg, Now: atClock(approveClock)}).ApproveAndApply(ctx, clone, nil)
	if err != nil || done.Status != proposals.Applied {
		t.Fatalf("ApproveAndApply = %+v, %v", done, err)
	}
	type job struct {
		name, prompt, schedule, tz, kind, createdBy string
		createdAt                                   float64
		thread, promptBy                            sql.NullString
	}
	read := func(where string) (j job) {
		t.Helper()
		err := d.Read.QueryRow("SELECT name, prompt, schedule, tz, kind, created_by, created_at, created_in_thread, prompt_by FROM jobs WHERE "+where).
			Scan(&j.name, &j.prompt, &j.schedule, &j.tz, &j.kind, &j.createdBy, &j.createdAt, &j.thread, &j.promptBy)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	if got, want := read("id = (SELECT MAX(id) FROM jobs)"), read("id = 1"); got != want {
		t.Errorf("job = %+v\nwant %+v", got, want)
	}
	var jobID int64
	if err := d.Read.QueryRow("SELECT MAX(id) FROM jobs").Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	var action, undo string
	var ts float64
	if err := d.Read.QueryRow("SELECT action, undo, ts FROM proposal_actions WHERE proposal_id = ?", clone).Scan(&action, &undo, &ts); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`{"type": "reminder", "job_id": %d}`, jobID); action != want {
		t.Errorf("action = %s, want %s", action, want)
	}
	if want := fmt.Sprintf(`{"op": "reminder_cancel", "job_id": %d}`, jobID); undo != want || ts != approveClock {
		t.Errorf("undo = %s at %v, want %s", undo, ts, want)
	}
	var result string
	if err := d.Read.QueryRow("SELECT result FROM proposals WHERE id = ?", clone).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`{"summary": "Reminder scheduled for 2026-10-16 12:05", "job_id": %d}`, jobID); result != want {
		t.Errorf("result = %s, want %s", result, want)
	}
}
