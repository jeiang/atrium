package openloops_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func reminders(d *db.DB) *openloops.Reminders {
	return &openloops.Reminders{DB: d, Now: NOW, Env: env()}
}

func pipeline(d *db.DB) *proposals.Pipeline {
	r := proposals.NewRegistry()
	openloops.Register(r, reminders(d))
	return &proposals.Pipeline{DB: d, Registry: r, Now: NOW}
}

func TestReminderProposalExecutorCreatesOneShotJob(t *testing.T) {
	d := newDB(t)
	mid := mail(t, d, 1, 1, nil)
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Send invoice', 'me', '2026-10-16', ?)", mid, NOW())
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Far away', 'me', '2026-12-31', ?)", mid, NOW())
	r := reminders(d)
	for i, want := range []int{1, 0} {
		if n, err := r.Propose(ctx, NOW(), tz); err != nil || n != want {
			t.Fatalf("Propose #%d = %d, %v; want %d", i+1, n, err, want)
		}
	}
	pending, err := proposals.List(ctx, d.Read, proposals.Pending, openloops.KindReminder)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	p := pending[0]
	preview := p.Preview.(py.Dict)
	if at, _ := preview.Get("remind_at"); !strings.HasPrefix(at.(string), "2026-10-16T09:00") {
		t.Errorf("remind_at = %v", at)
	}
	if p.Title != "Remind me: Send invoice (due 2026-10-16)" || p.Account != nil {
		t.Errorf("proposal = %+v", p)
	}
	done, err := pipeline(d).ApproveAndApply(ctx, p.ID, nil)
	if err != nil || done.Status != proposals.Applied {
		t.Fatalf("ApproveAndApply = %+v, %v", done, err)
	}
	jobs, err := scheduler.ListJobs(ctx, d.Read, false)
	if err != nil {
		t.Fatal(err)
	}
	var cron []scheduler.ListedJob
	for _, j := range jobs {
		if j.Kind == scheduler.JobCron {
			cron = append(cron, j)
		}
	}
	if len(cron) != 1 {
		t.Fatalf("cron jobs = %+v", jobs)
	}
	job := cron[0]
	if want := "Remind the operator: Send invoice (source mail 1)"; job.Prompt != want {
		t.Errorf("prompt = %q, want %q", job.Prompt, want)
	}
	if job.CreatedBy != scheduler.Operator || job.NextDue == nil {
		t.Fatalf("job = %+v", job.Job)
	}
	if got := time.Unix(int64(*job.NextDue), 0).In(mustZone(tz)).Format("2006-01-02 15:04"); got != "2026-10-16 09:00" {
		t.Errorf("next_due = %s", got)
	}
	result, _ := done.Result.(py.Dict)
	if summary, _ := result.Get("summary"); summary != "Reminder scheduled for 2026-10-16 09:00" {
		t.Errorf("result = %v", done.Result)
	}
	var linked int64
	if err := d.Read.QueryRow("SELECT proposal_id FROM commitments WHERE text = 'Send invoice'").Scan(&linked); err != nil || linked != p.ID {
		t.Errorf("commitment.proposal_id = %d, %v", linked, err)
	}
	undone, err := pipeline(d).Undo(ctx, p.ID)
	if err != nil || !undone.Undone {
		t.Fatalf("Undo = %+v, %v", undone, err)
	}
	if after, err := scheduler.GetJob(ctx, d.Read, job.ID); err != nil || after == nil || !after.Cancelled {
		t.Errorf("job after undo = %+v, %v", after, err)
	}
}

func TestReminderIsNotCreatedWithoutApproval(t *testing.T) {
	d := newDB(t)
	mid := mail(t, d, 1, 1, nil)
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'x', 'me', '2026-10-15', ?)", mid, NOW())
	if _, err := reminders(d).Propose(ctx, NOW(), tz); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, "SELECT COUNT(*) FROM jobs WHERE kind = 'cron'"); got != 0 {
		t.Errorf("cron jobs = %d", got)
	}
}

func TestReminderWindowAndTimes(t *testing.T) {
	d := newDB(t)
	mid := mail(t, d, 1, 1, nil)
	for due, text := range map[string]string{
		"2026-10-14": "yesterday", "2026-10-01": "overdue 14", "2026-09-30": "too old", "2026-10-16": "tomorrow", "2026-10-17": "later",
	} {
		exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, ?, 'me', ?, ?)", mid, text, due, NOW())
	}
	n, err := reminders(d).Propose(ctx, NOW(), "")
	if err != nil || n != 3 {
		t.Errorf("Propose = %d, %v; want 3 (due within a day ahead and 14 days back)", n, err)
	}
	if at, err := openloops.ReminderTime("2026-10-14", tz, NOW()); err != nil || at != NOW()+300 {
		t.Errorf("a passed hour reminds in five minutes: %v, %v", at, err)
	}
	want := float64(time.Date(2026, 10, 16, 9, 0, 0, 0, mustZone(tz)).Unix())
	if at, err := openloops.ReminderTime("2026-10-16", tz, NOW()); err != nil || at != want {
		t.Errorf("ReminderTime = %v, %v; want %v", at, err, want)
	}
	if _, err := openloops.ReminderTime("2026-10-16", "Nowhere/Land", NOW()); err == nil {
		t.Error("unknown zone accepted")
	}
}

func TestReminderPayloadAndFingerprint(t *testing.T) {
	d := newDB(t)
	mid := mail(t, d, 1, 1, nil)
	exec(t, d, "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Send invoice', 'me', '2026-10-16', ?)", mid, NOW())
	if _, err := reminders(d).Propose(ctx, NOW(), tz); err != nil {
		t.Fatal(err)
	}
	var payload, preview, fingerprint string
	if err := d.Read.QueryRow("SELECT payload, preview, fingerprint FROM proposals").Scan(&payload, &preview, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if want := `{"commitment_id": 1, "text": "Send invoice", "due": "2026-10-16", "owner": "me", "message_id": 1, "tz": "America/Port_of_Spain"}`; payload != want {
		t.Errorf("payload = %s", payload)
	}
	if want := `{"commitment_id": 1, "text": "Send invoice", "due": "2026-10-16", "owner": "me", "message_id": 1, "tz": "America/Port_of_Spain", "subject": "Hello", "remind_at": "2026-10-16T09:00-04:00"}`; preview != want {
		t.Errorf("preview = %s", preview)
	}
	if fingerprint != "47bb01f13f96332b34a6c369ac630e31" {
		t.Errorf("fingerprint = %s", fingerprint)
	}
}
