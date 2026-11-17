package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/calendar"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func preUpgrade(t *testing.T, r *mailClassifierRig, statuses ...string) {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "schema", "event_candidates.sql"))
	if err != nil {
		t.Fatal(err)
	}
	old := float64(time.Now().Add(-time.Hour).Unix())
	for _, q := range []string{"DELETE FROM schema_migrations WHERE version = 8", string(ddl), "DELETE FROM mail_scans WHERE kind = 'events'"} {
		if _, err := r.d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i, m := range r.targets {
		if _, err := r.d.Write.Exec("UPDATE messages SET first_seen = ? WHERE id = ?", old, m.id); err != nil {
			t.Fatal(err)
		}
		if _, err := r.d.Write.Exec("INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (?, 'judged before the upgrade', ?, ?)", m.id, statuses[i%len(statuses)], old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationDropsEventCandidatesAndNeverRejudgesOlderMail(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	preUpgrade(t, r, "none", "drafted", "reviewed")
	proposals := r.count(t, "SELECT COUNT(*) FROM proposals")

	r.syncAll(t)
	if n := r.count(t, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'event_candidates'"); n != 0 {
		t.Fatalf("event_candidates survived migration 8")
	}
	if n := r.count(t, "SELECT COUNT(*) FROM schema_migrations WHERE version = 8"); n != 1 {
		t.Fatalf("migration 8 recorded %d times, want 1", n)
	}
	if calls := r.targetCalls(t, 0); len(calls["event"]) != 0 {
		t.Fatalf("mail judged before the upgrade was judged again: %v", calls["event"])
	}
	if n := r.count(t, "SELECT COUNT(*) FROM proposals"); n != proposals {
		t.Fatalf("the first pass after the upgrade made %d proposals, want none", n-proposals)
	}
	for _, m := range r.targets {
		if got := r.event(t, m.id); got != "" {
			t.Errorf("older message %q has event state %q after the upgrade", m.subject, got)
		}
	}

	const fresh = "Lunch with Sam on Thursday"
	before := len(r.requests(t))
	h, x := mailtest.RawWith(fresh, "sam@elsewhere.test", "fresh-event", "Lunch at noon on Thursday?")
	r.f.fakes["icloud"].Boxes["INBOX"].Box.Add(h, x, nil, nil)
	r.syncAll(t)
	var id int64
	if err := r.d.Read.QueryRow("SELECT id FROM messages WHERE account = 'icloud' AND subject = ?", fresh).Scan(&id); err != nil {
		t.Fatal(err)
	}
	asked := 0
	for _, req := range r.requests(t)[before:] {
		if req.caller == "event" {
			if req.subject != fresh {
				t.Errorf("the second pass judged %q for events", req.subject)
			}
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("mail after the upgrade was judged %d times, want once", asked)
	}
	if got := r.event(t, id); got != "none" {
		t.Errorf("mail after the upgrade has event state %q, want none", got)
	}
	r.noMailWrites(t)
}

func TestMigrationFailsClosedWhileACandidateIsQueued(t *testing.T) {
	t.Parallel()
	r := newMailClassifierRig(t, nil)
	preUpgrade(t, r, "none", "queued", "reviewed")

	code, _, stderr := r.f.Run(context.Background(), "settings", "list")
	if code == cli.ExitOK || !strings.Contains(stderr, "1 event candidates are still queued") {
		t.Fatalf("opening a database with a queued candidate: exit %d stderr %q", code, stderr)
	}
	if n := r.count(t, "SELECT COUNT(*) FROM schema_migrations WHERE version = 8"); n != 0 {
		t.Fatalf("migration 8 was recorded with a queued candidate")
	}
	if n := r.count(t, "SELECT COUNT(*) FROM event_candidates"); n != len(r.targets) {
		t.Fatalf("event_candidates holds %d rows after the refused migration, want %d", n, len(r.targets))
	}
	if n := len(r.requests(t)); n != 0 {
		t.Fatalf("the refused upgrade sent %d model requests", n)
	}
}

func TestMailEventProposalApprovalWritesOnlyToCalDAV(t *testing.T) {
	dir, srv := davFixture(t)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	p := &calendar.Proposals{DB: d, Now: calendar.Now}
	raw := map[string]any{"title": "Lunch with Sam", "start": "2027-10-14T12:00:00", "end": "2027-10-14T13:00:00", "all_day": false, "calendar": string(calendar.ICloud)}
	source := map[string]any{"message_id": 41, "subject": "Lunch with Sam on Thursday", "from": "sam@elsewhere.test", "invitation": false}
	ctx, _, _ := bootedState(t, testLookupFor(t))
	ctx = proposals.WithHooks(audit.With(ctx, "fixture", audit.UI), proposals.Hooks{})
	id, created, err := p.Propose(ctx, raw, source, "mail:41")
	if err != nil || !created {
		t.Fatalf("propose: %v created %v", err, created)
	}
	if _, again, err := p.Propose(ctx, raw, source, "mail:41"); err != nil || again {
		t.Fatalf("a second proposal for one message: %v created %v", err, again)
	}
	if n := len(srv.Objects()); n != 0 {
		t.Fatalf("proposing wrote %d calendar objects", n)
	}
	if got := runApp(t, "proposals", "approve", strconv.FormatInt(id, 10)); got.code != cli.ExitOK {
		t.Fatalf("approve: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if n := len(srv.Objects()); n != 1 {
		t.Fatalf("approval wrote %d calendar objects, want 1: %v", n, srv.Objects())
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", id).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("proposal status %q %v, want applied", status, err)
	}
}
