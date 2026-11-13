package main

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/proposals"
)

func TestDeletionApprovalRefusedByAHeldPassStaysApprovable(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	m.add("INBOX", "Weekly deals", "deals@shop.test", "promo", "Everything is on sale.", `\Seen`)
	id, uid, _ := m.racePending(t)
	promo := m.messageID(t, "Weekly deals")
	ctx := testProposalContext()
	snaps, err := mailops.Snapshots(ctx, d.Read, []int64{promo})
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshot of the promo message: %v (%d)", err, len(snaps))
	}
	account := "icloud"
	rule := map[string]any{"id": "del-deals", "account": account, "action": "delete",
		"when": []any{map[string]any{"field": "from_addr", "op": "eq", "value": "deals@shop.test"}}}
	pid, _, err := proposals.Create(ctx, d, "deletion_rule", &account, "Delete deals",
		map[string]any{"rule": rule, "reason": "deleted", "messages": scanutil.SnapshotList(snaps)},
		map[string]any{"backlog": 1}, "del-deals", mail.WallClock())
	if err != nil {
		t.Fatal(err)
	}
	proposal := strconv.FormatInt(pid, 10)
	serveWatching(t, m.dir, d, m.fake)
	m.correctToBills(t, id, uid)
	manual, release := m.holdPassAtCopy(t, d)

	started := time.Now()
	refused := runApp(t, "proposals", "approve", proposal)
	waited := time.Since(started)
	if refused.code == cli.ExitOK || !strings.Contains(refused.stderr, mail.PassResource) {
		t.Fatalf("approval beside a pass held at its COPY: exit %d, stderr %q; want a refusal naming %s", refused.code, refused.stderr, mail.PassResource)
	}
	if waited < db.ShortLockWait {
		t.Fatalf("the approval was refused after %s, want it to wait %s for mail-pass", waited, db.ShortLockWait)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", pid); status != "pending" {
		t.Fatalf("the refused proposal is %s, want pending", status)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE proposal_id = ?", pid); n != 0 {
		t.Fatalf("%d effects recorded for the refused approval, want none", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM rules WHERE id = 'del-deals'"); n != 0 {
		t.Fatalf("the refused approval created %d rules, want none", n)
	}

	release()
	waitUntil(t, 30*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if n := len(m.fake.Boxes["Bills"].Box.Messages); n != 1 {
		t.Fatalf("Bills holds %d messages after the pass, want 1", n)
	}
	if again := runApp(t, "proposals", "approve", proposal); again.code != cli.ExitOK {
		t.Fatalf("approval after the pass released mail-pass: exit %d, stderr %q", again.code, again.stderr)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", pid); status != "applied" {
		t.Fatalf("the proposal is %s after its second approval, want applied", status)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM rules WHERE id = 'del-deals'"); n != 1 {
		t.Fatalf("%d deletion rules after the second approval, want 1", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM effects WHERE proposal_id = ? AND operation = 'rule_create' AND state = 'done'", pid); n != 1 {
		t.Fatalf("%d rule_create effects, want exactly 1", n)
	}
	if n := len(m.fake.Boxes["Deleted Messages"].Box.Messages); n != 1 {
		t.Fatalf("Trash holds %d messages after the approval, want 1", n)
	}
	if third := runApp(t, "proposals", "approve", proposal); third.code == cli.ExitOK {
		t.Fatal("a third approval of an applied proposal succeeded")
	}
}

func TestPhoneApprovalRefusedByAHeldPassKeepsItsLink(t *testing.T) {
	m := mailSafetyFixture(t, "live")
	d := mustConnect(t, m.path, keyFor(t))
	declareJobsAndParkThem(t, d)
	m.add("INBOX", "Weekly deals", "deals@shop.test", "promo", "Everything is on sale.", `\Seen`)
	id, uid, _ := m.racePending(t)
	promo := m.messageID(t, "Weekly deals")
	for key, value := range map[string]string{policy.EnabledKey: "true", policy.KindsKey: executors.MailTriage} {
		if ran := runApp(t, "settings", "set", key, value); ran.code != cli.ExitOK {
			t.Fatalf("settings set %s: %d %s", key, ran.code, ran.stderr)
		}
	}
	ctx := testProposalContext()
	account := "icloud"
	now := mail.WallClock()
	pid, _, err := proposals.Create(ctx, d, executors.MailTriage, &account, "Archive deals",
		map[string]any{"items": []any{map[string]any{"id": promo, "action": executors.TriageArchive}}},
		map[string]any{"count": 1}, "archive-deals", now)
	if err != nil {
		t.Fatal(err)
	}
	prop, err := proposals.Get(ctx, d.Read, pid)
	if err != nil || prop == nil {
		t.Fatalf("the triage proposal: %v", err)
	}
	approve, _, err := actionlink.MintPair(ctx, d.Write, pid, prop.Fingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	base := serveWatching(t, m.dir, d, m.fake)
	m.correctToBills(t, id, uid)
	manual, release := m.holdPassAtCopy(t, d)
	client := mrvClient(t)
	tap := func() (int, string) {
		t.Helper()
		code, body, _ := mrvDo(t, client, http.MethodPost, base+"/actions/"+approve, nil)
		return code, body
	}
	effects := func() int {
		t.Helper()
		return m.count(t, "SELECT COUNT(*) FROM effects WHERE proposal_id = ?", pid)
	}

	if code, body := tap(); code != http.StatusServiceUnavailable || !strings.Contains(body, mail.PassResource) {
		t.Fatalf("phone approval beside a pass held at its COPY = %d %q; want 503 naming %s", code, body, mail.PassResource)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", pid); status != "pending" {
		t.Fatalf("the refused proposal is %s, want pending", status)
	}
	if n := effects(); n != 0 {
		t.Fatalf("%d effects recorded for the refused phone approval, want none", n)
	}
	if n := m.count(t, "SELECT COUNT(*) FROM action_links WHERE proposal_id = ? AND used_ts IS NULL", pid); n != 1 {
		t.Fatalf("%d unused links after the busy refusal, want the link kept", n)
	}

	release()
	waitUntil(t, 30*time.Second, "the scheduled mail sync run", scheduledRunEnded(d, passrun.Sync, manual))
	if code, body := tap(); code != http.StatusOK {
		t.Fatalf("phone approval after the pass released mail-pass = %d %q", code, body)
	}
	if status := m.scalar(t, "SELECT status FROM proposals WHERE id = ?", pid); status != "applied" {
		t.Fatalf("the proposal is %s after its phone approval, want applied", status)
	}
	applied := effects()
	if applied != 1 {
		t.Fatalf("%d effects after the phone approval, want exactly 1", applied)
	}
	if code, _ := tap(); code != http.StatusGone {
		t.Fatalf("a third tap of the same link = %d, want 410", code)
	}
	if n := effects(); n != applied {
		t.Fatalf("%d effects after the third tap, want %d", n, applied)
	}
}

func TestSentCopyIsFiledWhileAPassHoldsMailPass(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	smtp := mrvSMTPServer(t)
	toolResult(t, "draft_email_create", map[string]any{"account": "icloud", "to": []any{"friend@x.test"}, "subject": "Lunch plans", "body": "Noon works."})
	dbPath := filepath.Join(m.dir, "atrium.db")
	id := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lunch plans'")
	draft := "/drafts/" + id
	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	code, page, _ := mrvDo(t, client, http.MethodGet, base+draft, nil)
	found := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(page)
	if code != http.StatusOK || found == nil {
		t.Fatalf("draft page = %d without a revision: %.400s", code, page)
	}

	held, err := db.ResourceLocks(context.Background(), dbPath, []string{mail.PassResource}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	started := time.Now()
	code, body, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {found[1]}}, "HX-Request", "true")
	if code != http.StatusOK || strings.Contains(body, "Sent copy failed") {
		t.Fatalf("send while a pass holds mail-pass = %d: %.400s", code, body)
	}
	if waited := time.Since(started); waited >= db.ShortLockWait {
		t.Fatalf("the send took %s, want the Sent copy not to wait for mail-pass", waited)
	}
	if n := len(smtp.sent()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
	if state := mrvScalar(t, dbPath, "SELECT state FROM effects WHERE operation = 'sent_append' AND direction = 'forward'"); state != "done" {
		t.Fatalf("the Sent copy is %s, want done", state)
	}
	if n := len(m.fake.Boxes["Sent Messages"].Box.Messages); n != 1 {
		t.Fatalf("Sent holds %d messages, want 1", n)
	}
}
