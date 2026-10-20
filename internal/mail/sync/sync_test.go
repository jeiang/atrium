package sync_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
)

const gmailAllMail = "[Gmail]/All Mail"

func TestMessageStateGmailLeafAndInbox(t *testing.T) {
	st := mailsync.MessageState("gmail", "allmail", "x", []string{`\Seen`}, []string{`\Inbox`, "A", "A/B"})
	if st.InInbox != 1 || st.Seen != 1 || st.Folder == nil || *st.Folder != "A/B" || st.Location != "A/B" {
		t.Errorf("leaf state = %+v", st)
	}
	multi := mailsync.MessageState("gmail", "allmail", "x", nil, []string{"A", "C"})
	if multi.Folder != nil || multi.Location != "A|C" || multi.Seen != 0 || multi.Flags != "[]" {
		t.Errorf("multi state = %+v", multi)
	}
	if sent := mailsync.MessageState("gmail", "allmail", "x", nil, []string{`\Sent`, "A"}); sent.Sent != 1 {
		t.Errorf("sent state = %+v", sent)
	}
	if draft := mailsync.MessageState("gmail", "allmail", "x", []string{`\Draft`}, nil); draft.Sent != 1 || draft.Location != "(none)" || draft.Labels != "[]" {
		t.Errorf("draft state = %+v", draft)
	}
	if none := mailsync.MessageState("gmail", "allmail", "x", nil, []string{`\Inbox`}); none.Location != "INBOX" || none.Folder != nil {
		t.Errorf("inbox-only state = %+v", none)
	}
	ic := mailsync.MessageState("icloud", "filed", "Bills", []string{`\Seen`, `\Flagged`, `\Answered`}, []string{"ignored"})
	if ic.Flags != `["\\Answered", "\\Flagged", "\\Seen"]` || ic.Labels != "[]" || ic.Folder == nil || *ic.Folder != "Bills" || ic.Flagged != 1 || ic.InInbox != 0 {
		t.Errorf("icloud state = %+v", ic)
	}
	if sent := mailsync.MessageState("icloud", "sent", "Sent Messages", nil, nil); sent.Sent != 1 || sent.Folder != nil {
		t.Errorf("icloud sent state = %+v", sent)
	}
}

func TestFirstSyncSkipsUnindexedSystemMailboxesAndStoresRaw(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	add(boxes["INBOX"], "one", []string{`\Seen`}, nil)
	header, text := mailtest.RawWith("two", "b@bills.test", "two", "hello")
	boxes["Bills"].Add(header, text, nil, nil)
	add(boxes["Sent Messages"], "sent", nil, nil)
	stats := h.sync("icloud")
	if stats.New != 3 || stats.Mailboxes != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	var names []string
	for _, r := range h.query("SELECT name FROM mailboxes ORDER BY name") {
		names = append(names, r["name"].(string))
	}
	if want := []string{"Bills", "INBOX", "Misc", "Sent Messages"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("mailboxes = %v, want %v", names, want)
	}
	if got := h.messages("location = 'INBOX'")[0]["in_inbox"]; got != int64(1) {
		t.Errorf("inbox in_inbox = %v", got)
	}
	bills := h.messages("location = 'Bills'")[0]
	if bills["folder"] != "Bills" || bills["in_inbox"] != int64(0) || bills["from_domain"] != "bills.test" {
		t.Errorf("bills row = %v", bills)
	}
	rel := bills["raw_path"].(string)
	if rel != "raw/icloud/"+itoa(bills["mailbox_id"])+"/1_1.eml" {
		t.Errorf("raw_path = %q", rel)
	}
	raw, err := os.ReadFile(filepath.Join(h.cfg.CacheDir, rel))
	if err != nil || !bytes.HasPrefix(raw, []byte("From: b@bills.test")) || !bytes.Equal(raw, append(slices.Clone(header), text...)) {
		t.Errorf("raw file = %q, %v", raw, err)
	}
	info, err := os.Stat(filepath.Join(h.cfg.CacheDir, rel))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("raw file mode = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.CacheDir, rel+".tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: %v", err)
	}
	if n := h.count("SELECT COUNT(*) FROM message_fts"); n != 3 {
		t.Errorf("fts rows = %d", n)
	}
	inbox := h.query("SELECT * FROM mailboxes WHERE name = 'INBOX'")[0]
	if inbox["uidvalidity"] != int64(1) || inbox["highest_uid"] != int64(1) || inbox["highestmodseq"] != int64(boxes["INBOX"].ModSeq) {
		t.Errorf("inbox mailbox row = %v", inbox)
	}
	if last := h.query("SELECT last_sync_at FROM accounts WHERE name = 'icloud'")[0]["last_sync_at"]; last != h.clock {
		t.Errorf("last_sync_at = %v", last)
	}
}

func itoa(v any) string { return strconv.FormatInt(v.(int64), 10) }

func TestInboxIsSyncedLast(t *testing.T) {
	h := newHarness(t, "shadow")
	h.icloudBoxes()
	rec := &selectRecorder{Client: h.fake}
	if _, err := h.s.Account(context.Background(), "icloud", rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.selected; len(got) != 4 || got[len(got)-1] != "INBOX" || !slices.IsSorted(got[:3]) {
		t.Fatalf("selected = %v", got)
	}
}

func TestIncrementalSyncFetchesOnlyNewAndFlagDeltas(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	u1 := add(boxes["INBOX"], "one", nil, nil)
	h.sync("icloud")
	h.fake.Fetches = nil
	if stats := h.sync("icloud"); stats.New != 0 {
		t.Fatalf("second sync stats = %+v", stats)
	}
	if len(h.fake.Fetches) != 0 {
		t.Fatalf("unchanged mailbox fetched %v", h.fake.Fetches)
	}
	add(boxes["INBOX"], "two", nil, nil)
	boxes["INBOX"].SetFlags(u1, []string{`\Seen`}, nil)
	h.clock += 60
	stats := h.sync("icloud")
	if stats.New != 1 || stats.FlagChanges != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	var changed []uint64
	for _, f := range h.fake.Fetches {
		if f.Items.ChangedSince != 0 {
			changed = append(changed, f.Items.ChangedSince)
			if !f.UIDs.All() || f.Items.Header || f.Items.Body {
				t.Errorf("flag fetch = %+v", f)
			}
		}
	}
	if !reflect.DeepEqual(changed, []uint64{2}) {
		t.Errorf("CHANGEDSINCE fetches = %v, want [2]", changed)
	}
	if got := h.messages("uid = 1 AND location = 'INBOX'")[0]["seen"]; got != int64(1) {
		t.Errorf("seen = %v", got)
	}
	ev := h.query("SELECT * FROM events WHERE kind = 'flags'")
	if len(ev) != 1 || ev[0]["detail"] != `{"flags": [[], ["\\Seen"]], "location": ["INBOX", "INBOX"], "in_inbox": [1, 1]}` || ev[0]["ts"] != h.clock {
		t.Errorf("events = %v", ev)
	}
}

func TestNoCondstoreFallsBackToFullFlagScan(t *testing.T) {
	h := newHarness(t, "shadow")
	h.fake.Caps = map[string]bool{"IDLE": true}
	boxes := h.icloudBoxes()
	u1 := add(boxes["INBOX"], "one", nil, nil)
	h.sync("icloud")
	boxes["INBOX"].SetFlags(u1, []string{`\Flagged`}, nil)
	h.fake.Fetches = nil
	if stats := h.sync("icloud"); stats.FlagChanges != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(h.fake.Fetches) != 1 || h.fake.Fetches[0].Items.ChangedSince != 0 || !reflect.DeepEqual(h.fake.Fetches[0].UIDs.List(), []uint32{u1}) {
		t.Fatalf("fetches = %+v", h.fake.Fetches)
	}
}

func TestFlagFetchesAreBatched(t *testing.T) {
	h := newHarness(t, "shadow")
	h.fake.Caps = map[string]bool{}
	box := h.icloudBoxes()["Bills"]
	for i := 0; i < mailsync.FlagBatch+1; i++ {
		add(box, "m"+strconv.Itoa(i), nil, nil)
	}
	h.sync("icloud")
	h.fake.Fetches = nil
	h.sync("icloud")
	if len(h.fake.Fetches) != 2 || len(h.fake.Fetches[0].UIDs.List()) != mailsync.FlagBatch || len(h.fake.Fetches[1].UIDs.List()) != 1 {
		t.Fatalf("fetches = %d", len(h.fake.Fetches))
	}
}

func TestNewMessagesFetchInBatches(t *testing.T) {
	h := newHarness(t, "shadow")
	box := h.icloudBoxes()["Bills"]
	for i := 0; i < 90; i++ {
		add(box, "m"+strconv.Itoa(i), nil, nil)
	}
	if stats := h.sync("icloud"); stats.New != 90 {
		t.Fatalf("stats = %+v", stats)
	}
	var sizes []int
	for _, f := range h.fake.Fetches {
		if !f.Items.Header || !f.Items.Body || f.Items.Labels {
			t.Errorf("full fetch items = %+v", f.Items)
		}
		sizes = append(sizes, len(f.UIDs.List()))
	}
	if !reflect.DeepEqual(sizes, []int{40, 40, 10}) {
		t.Errorf("batch sizes = %v", sizes)
	}
}

func TestMoveBetweenMailboxesRecordedAsEvent(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	header, text := mailtest.Raw("move me")
	u1 := boxes["INBOX"].Add(header, text, []string{`\Seen`}, nil)
	h.sync("icloud")
	boxes["INBOX"].Remove(u1)
	boxes["Bills"].Add(header, text, []string{`\Seen`}, nil)
	h.clock += 60
	stats := h.sync("icloud")
	if len(stats.GoneIDs) != 1 {
		t.Fatalf("gone = %v", stats.GoneIDs)
	}
	ev := h.query("SELECT * FROM events WHERE kind = 'move'")
	if len(ev) != 1 || ev[0]["detail"] != `{"from": "INBOX", "to": "Bills", "to_id": 2}` || ev[0]["message_id"] != int64(stats.GoneIDs[0]) || ev[0]["ts"] != h.clock {
		t.Fatalf("events = %v", ev)
	}
	gone := h.messages("gone_at IS NOT NULL")
	if len(gone) != 1 || gone[0]["location"] != "INBOX" || gone[0]["gone_at"] != h.clock {
		t.Fatalf("gone rows = %v", gone)
	}
}

func TestDisappearanceWithoutCounterpartIsDelete(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	u1 := add(boxes["Bills"], "bye", nil, nil)
	h.sync("icloud")
	boxes["Bills"].Remove(u1)
	h.sync("icloud")
	ev := h.query("SELECT detail FROM events WHERE kind = 'delete'")
	if len(ev) != 1 || ev[0]["detail"] != `{"from": "Bills"}` {
		t.Fatalf("events = %v", ev)
	}
}

func TestMoveDetectionIsPerAccount(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	header, text := mailtest.Raw("same bytes")
	u1 := boxes["Bills"].Add(header, text, nil, nil)
	h.sync("icloud")
	boxes["Bills"].Remove(u1)
	if _, err := h.d.Write.Exec(`INSERT INTO accounts (name, kind) VALUES ('gmail', 'gmail')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Write.Exec(`INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (99, 'gmail', 'x', 'x', 'allmail')`); err != nil {
		t.Fatal(err)
	}
	digest := h.messages("1=1")[0]["digest"]
	if _, err := h.d.Write.Exec(`INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, location) VALUES ('gmail', 99, 1, 1, ?, 0, 'x')`, digest); err != nil {
		t.Fatal(err)
	}
	h.sync("icloud")
	if n := h.count("SELECT COUNT(*) FROM events WHERE kind = 'delete'"); n != 1 {
		t.Fatalf("delete events = %d", n)
	}
}

func TestUIDValidityChangeInvalidatesWithoutEvents(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	add(boxes["Bills"], "a", nil, nil)
	h.sync("icloud")
	boxes["Bills"].Validity = 2
	h.clock += 60
	h.sync("icloud")
	if n := h.count("SELECT COUNT(*) FROM events"); n != 0 {
		t.Fatalf("events = %d", n)
	}
	live := h.messages("gone_at IS NULL")
	if len(live) != 1 || live[0]["uidvalidity"] != int64(2) || len(h.messages("1=1")) != 2 {
		t.Fatalf("live rows = %v", live)
	}
	mbx := h.query("SELECT * FROM mailboxes WHERE name = 'Bills'")[0]
	if mbx["uidvalidity"] != int64(2) || mbx["highest_uid"] != int64(1) {
		t.Errorf("mailbox = %v", mbx)
	}
	raw := h.messages("uidvalidity = 2")[0]["raw_path"].(string)
	if !strings.HasSuffix(raw, "/2_1.eml") {
		t.Errorf("raw_path = %q", raw)
	}
}

func TestHighestUIDNeverDecreases(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	add(boxes["Bills"], "a", nil, nil)
	u2 := add(boxes["Bills"], "b", nil, nil)
	h.sync("icloud")
	boxes["Bills"].Remove(u2)
	h.sync("icloud")
	if got := h.query("SELECT highest_uid FROM mailboxes WHERE name = 'Bills'")[0]["highest_uid"]; got != int64(2) {
		t.Fatalf("highest_uid = %v", got)
	}
}

func TestGmailLabelsCategoriesAndLabelEvents(t *testing.T) {
	h := newHarness(t, "shadow")
	h.fake.Gmail = true
	box := h.fake.AddBox(gmailAllMail, mailtest.NewBox(1))
	h.fake.AddBox("INBOX", mailtest.NewBox(1))
	h.fake.AddBox("[Gmail]/Sent Mail", mailtest.NewBox(1))
	u1 := add(box, "n1", []string{`\Seen`}, []string{`\Inbox`, "Personal", "Personal/Friends"})
	u2 := add(box, "n2", []string{`\Seen`}, []string{`\Inbox`})
	h.fake.CategoryUIDs = map[string][]uint32{"updates": {u1}, "promotions": {u2}}
	h.sync("gmail")
	first := h.messages("uid = ?", u1)[0]
	if first["folder"] != "Personal/Friends" || first["in_inbox"] != int64(1) || first["category"] != "updates" || first["gm_msgid"] != "g1" || first["gm_thrid"] != "t1" {
		t.Errorf("first = %v", first)
	}
	if first["labels"] != `["\\Inbox", "Personal", "Personal/Friends"]` {
		t.Errorf("labels = %v", first["labels"])
	}
	second := h.messages("uid = ?", u2)[0]
	if second["folder"] != nil || second["category"] != "promotions" || second["location"] != "INBOX" {
		t.Errorf("second = %v", second)
	}
	var names []string
	for _, r := range h.query("SELECT name FROM mailboxes") {
		names = append(names, r["name"].(string))
	}
	if !reflect.DeepEqual(names, []string{gmailAllMail}) {
		t.Fatalf("mailboxes = %v", names)
	}
	var searched []string
	for _, c := range h.fake.Searches {
		if c.GmailRaw != "" {
			searched = append(searched, c.GmailRaw)
		}
	}
	if want := []string{"category:social", "category:promotions", "category:updates", "category:forums"}; !reflect.DeepEqual(searched, want) {
		t.Errorf("category searches = %v", searched)
	}
	box.SetFlags(u2, nil, []string{"Alerts/Porkbun"})
	h.fake.CategoryUIDs = map[string][]uint32{"updates": {u1}}
	h.sync("gmail")
	moved := h.messages("uid = ?", u2)[0]
	if moved["folder"] != "Alerts/Porkbun" || moved["in_inbox"] != int64(0) || moved["category"] != "primary" {
		t.Errorf("relabelled = %v", moved)
	}
	ev := h.query("SELECT * FROM events WHERE kind = 'labels'")
	if len(ev) != 1 || ev[0]["detail"] != `{"flags": [["\\Seen"], ["\\Seen"]], "location": ["INBOX", "Alerts/Porkbun"], "in_inbox": [1, 0]}` {
		t.Errorf("events = %v", ev)
	}
	for _, f := range h.fake.Fetches {
		if !f.Items.Labels {
			t.Errorf("gmail fetch without labels: %+v", f.Items)
		}
	}
}

func TestSentMailboxIsIndexedSinceCutoffAndMarkedSent(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	add(boxes["Sent Messages"], "outgoing", nil, nil)
	h.sync("icloud")
	row := h.messages("location = 'Sent Messages'")
	if len(row) != 1 || row[0]["sent"] != int64(1) || row[0]["in_inbox"] != int64(0) || row[0]["folder"] != nil || row[0]["in_reply_to"] != "" || row[0]["refs"] != "" {
		t.Fatalf("sent row = %v", row)
	}
	if role := h.query("SELECT role FROM mailboxes WHERE name = 'Sent Messages'")[0]["role"]; role != "sent" {
		t.Errorf("role = %v", role)
	}
	var since []time.Time
	all := 0
	for _, c := range h.fake.Searches {
		if !c.Since.IsZero() {
			since = append(since, c.Since)
		} else {
			all++
		}
	}
	want := time.Unix(int64(h.clock), 0).UTC().AddDate(0, 0, -mailsync.SentBackfillDays)
	if len(since) != 1 || since[0].Location() != time.UTC || since[0].Hour() != 0 || since[0].Sub(want).Abs() > 24*time.Hour {
		t.Fatalf("SINCE searches = %v, want near %v", since, want)
	}
	if all != 3 {
		t.Errorf("full searches = %d, want 3", all)
	}
	h.sync("icloud")
	if gone := h.messages("sent = 1 AND gone_at IS NOT NULL"); len(gone) != 0 {
		t.Errorf("sent mail looks vanished: %v", gone)
	}
}

func TestSentSinceReachesBackToOldestIndexedMail(t *testing.T) {
	h := newHarness(t, "shadow")
	h.icloudBoxes()
	h.sync("icloud")
	mbx := h.query("SELECT id FROM mailboxes WHERE name = 'Sent Messages'")[0]["id"].(int64)
	ctx := context.Background()
	run := h.clock
	got, err := h.s.SentSince(ctx, mbx, 1, run)
	if err != nil {
		t.Fatal(err)
	}
	if want := mailsync.IMAPDate(time.Unix(int64(run-90*86400), 0)); mailsync.IMAPDate(got) != want {
		t.Fatalf("SentSince = %s, want %s", mailsync.IMAPDate(got), want)
	}
	if _, err := h.d.Write.Exec(`INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, date_ts, first_seen, sent) VALUES ('icloud', ?, 1, 99, 'z', ?, 0, 1)`,
		mbx, run-200*86400); err != nil {
		t.Fatal(err)
	}
	got, err = h.s.SentSince(ctx, mbx, 1, run)
	if err != nil {
		t.Fatal(err)
	}
	if want := mailsync.IMAPDate(time.Unix(int64(run-202*86400), 0)); mailsync.IMAPDate(got) != want {
		t.Fatalf("SentSince = %s, want %s", mailsync.IMAPDate(got), want)
	}
}

func TestIngestStoresReplyHeaders(t *testing.T) {
	h := newHarness(t, "shadow")
	boxes := h.icloudBoxes()
	header := []byte("From: a@x.test\r\nTo: me@x.test\r\nSubject: Re: hi\r\nMessage-ID: <c@x.test>\r\n" +
		"In-Reply-To: <b@x.test>\r\nReferences: <a@x.test> <b@x.test>\r\nDate: Tue, 06 Oct 2026 10:00:00 +0000\r\n\r\n")
	boxes["INBOX"].Add(header, []byte("hello"), nil, nil)
	h.sync("icloud")
	row := h.messages("location = 'INBOX'")
	if len(row) != 1 || row[0]["in_reply_to"] != "b@x.test" || row[0]["refs"] != "a@x.test b@x.test" || row[0]["msgid"] != "c@x.test" ||
		row[0]["msgid_domain"] != "x.test" || row[0]["subject_tmpl"] != "hi" || row[0]["to_addrs"] != `["me@x.test"]` ||
		row[0]["date_ts"] != float64(1791280800) || row[0]["size"] != int64(len(header)+5) || row[0]["internaldate"] != "06-Oct-2026 10:00:00 +0000" ||
		row[0]["gm_msgid"] != nil || row[0]["first_seen"] != h.clock || row[0]["body_len"] != int64(5) {
		t.Fatalf("row = %v", row)
	}
	fts := h.query("SELECT subject, sender, body FROM message_fts WHERE rowid = ?", row[0]["id"])
	if len(fts) != 1 || fts[0]["subject"] != "Re: hi" || fts[0]["sender"] != " <a@x.test>" || fts[0]["body"] != "hello" {
		t.Fatalf("fts = %v", fts)
	}
}

func TestSyncIsReadOnlyEvenInLiveMode(t *testing.T) {
	h := newHarness(t, "live")
	boxes := h.icloudBoxes()
	u1 := add(boxes["INBOX"], "one", nil, nil)
	add(boxes["Bills"], "two", nil, nil)
	add(boxes["Sent Messages"], "three", nil, nil)
	h.sync("icloud")
	boxes["INBOX"].SetFlags(u1, []string{`\Seen`}, nil)
	boxes["Bills"].Remove(1)
	h.sync("icloud")
	h.fake.Gmail = true
	gm := h.fake.AddBox(gmailAllMail, mailtest.NewBox(1))
	add(gm, "g", nil, []string{`\Inbox`})
	h.sync("gmail")
	f := h.fake
	if len(f.Writes) != 0 || len(f.Created) != 0 || len(f.Deleted) != 0 || len(f.Appended) != 0 || f.RW != "" {
		t.Fatalf("sync wrote: writes %v created %v deleted %v appended %d rw %q", f.Writes, f.Created, f.Deleted, len(f.Appended), f.RW)
	}
}

func TestOneOpensSyncsClosesAndLogs(t *testing.T) {
	h := newHarness(t, "shadow")
	add(h.icloudBoxes()["INBOX"], "one", nil, nil)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	stats, err := h.s.One(context.Background(), "icloud", func(context.Context, string) (mail.Client, error) { return h.fake, nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.New != 1 || !h.fake.Closed {
		t.Fatalf("stats = %+v closed = %v", stats, h.fake.Closed)
	}
	if !strings.Contains(buf.String(), "icloud: 1 new, 0 gone, 0 flag changes") {
		t.Errorf("log = %q", buf.String())
	}
}

func TestOneClosesTheClientOnFailure(t *testing.T) {
	h := newHarness(t, "shadow")
	h.fake.AddBox("Bills", mailtest.NewBox(1))
	delete(h.fake.Boxes, "Bills")
	_, err := h.s.One(context.Background(), "icloud", func(context.Context, string) (mail.Client, error) { return h.fake, nil })
	if err == nil || !h.fake.Closed {
		t.Fatalf("err = %v closed = %v", err, h.fake.Closed)
	}
}

func TestRoles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		account string
		want    string
	}{
		{"INBOX", nil, "icloud", "inbox"},
		{"Misc", nil, "icloud", "misc"},
		{"Bills/Utilities", nil, "icloud", "filed"},
		{"Whatever", []string{`\Trash`}, "icloud", "trash"},
		{"Junk", nil, "icloud", "junk"},
		{gmailAllMail, nil, "gmail", "allmail"},
		{"INBOX", nil, "gmail", "system"},
	} {
		if got := mail.MailboxRole(tc.name, tc.flags, tc.account); got != tc.want {
			t.Errorf("MailboxRole(%q, %v, %q) = %q, want %q", tc.name, tc.flags, tc.account, got, tc.want)
		}
	}
}

func TestIMAPDate(t *testing.T) {
	if got := mailsync.IMAPDate(time.Date(2026, 1, 5, 23, 59, 0, 0, time.UTC)); got != "05-Jan-2026" {
		t.Errorf("IMAPDate = %s", got)
	}
}
