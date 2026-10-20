package applier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/live/livetest"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	seen       = livetest.Seen
	flagged    = livetest.Flagged
	inboxLabel = mailops.InboxLabel
	allMail    = config.GmailAllMail
	gmailTrash = "[Gmail]/Trash"
)

var ctx = context.Background()

type hooked struct {
	*mailtest.FakeClient
	selectRW func(ctx context.Context, name string) error
	copy     func(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error)
}

func (h *hooked) SelectRW(ctx context.Context, name string) error {
	if h.selectRW != nil {
		return h.selectRW(ctx, name)
	}
	return h.FakeClient.SelectRW(ctx, name)
}

func (h *hooked) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	if h.copy != nil {
		return h.copy(ctx, uids, dest)
	}
	return h.FakeClient.Copy(ctx, uids, dest)
}

type world struct {
	*livetest.World
	client  *hooked
	applier *Applier
}

func wrap(w *livetest.World) *world {
	x := &world{World: w, client: &hooked{FakeClient: w.Fake}}
	x.applier = &Applier{
		Live: w.Live,
		Open: func(context.Context, string) (mail.Client, error) { return x.client, nil },
		Resync: func(ctx context.Context, account string) error {
			_, err := w.Syncer.Account(ctx, account, w.Fake)
			return err
		},
	}
	return x
}

func icloud(t *testing.T, mode string, seed ...livetest.Seed) *world {
	return wrap(livetest.ICloud(t, mode, seed...))
}

func gmail(t *testing.T, mode string, seed ...livetest.Seed) *world {
	return wrap(livetest.Gmail(t, mode, seed...))
}

type arrival struct {
	flags  []string
	labels []string
	box    string
	sender string
	noSync bool
}

func (w *world) arrive(subject string, opt arrival) uint32 {
	w.T.Helper()
	if opt.flags == nil {
		opt.flags = []string{seen}
	}
	if w.Account == config.Gmail && opt.labels == nil {
		opt.labels = []string{inboxLabel}
	}
	if opt.sender == "" {
		opt.sender = "a@shop.test"
	}
	box := w.InboxBox()
	if opt.box != "" {
		box = w.Fake.Boxes[opt.box]
	}
	header, text := mailtest.RawWith(subject, opt.sender, subject, "hello")
	uid := box.Add(header, text, opt.flags, opt.labels)
	if !opt.noSync {
		w.Sync()
	}
	return uid
}

func (w *world) decide(subject, dest string) int64 {
	w.T.Helper()
	return w.Decide(subject, "file", &dest, "learned")
}

func (w *world) apply() Outcome {
	w.T.Helper()
	out, err := w.applier.ApplyAccount(ctx, w.Account)
	if err != nil {
		w.T.Fatal(err)
	}
	return out
}

func (w *world) deletions(force bool) Outcome {
	w.T.Helper()
	out, err := w.applier.DeletionPass(ctx, w.Account, force)
	if err != nil {
		w.T.Fatal(err)
	}
	return out
}

func (w *world) verbs() []string {
	var out []string
	for _, x := range w.Fake.Writes {
		out = append(out, x.Verb)
	}
	return out
}

func (w *world) undo(id int64) live.UndoResult {
	w.T.Helper()
	res, err := w.Live.Undo(ctx, id)
	if err != nil {
		w.T.Fatal(err)
	}
	return res
}

func (w *world) flagDecision(subject string, match int) {
	w.T.Helper()
	w.Exec("INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, ?, 1, ?)", w.MID(subject), w.Account, match)
}

func (w *world) correct(subject, folder string) int64 {
	w.T.Helper()
	mid := w.MID(subject)
	if _, err := corrections.Record(ctx, w.DB, mid, folder, corrections.Source, w.Clock); err != nil {
		w.T.Fatal(err)
	}
	return mid
}

func (w *world) deleteRule(account string) {
	w.T.Helper()
	doc := `{"rules": [{"id": "r-del", "action": "delete", "when": [{"field": "from_domain", "op": "eq", "value": "spam.test"}, ` +
		`{"field": "age_days", "op": "gte", "value": 1}]`
	if account != "" {
		doc += `, "account": "` + account + `"`
	}
	parsed, err := py.JSONLoads([]byte(doc + "}]}"))
	if err != nil {
		w.T.Fatal(err)
	}
	if _, err := rules.ImportRules(ctx, w.DB, parsed); err != nil {
		w.T.Fatal(err)
	}
}

func (w *world) age(subject string, ts float64) {
	w.T.Helper()
	w.Exec("UPDATE messages SET date_ts = ? WHERE subject = ?", ts, subject)
}

func (w *world) count(query string, args ...any) int {
	w.T.Helper()
	var n int
	if err := w.DB.Read.QueryRow(query, args...).Scan(&n); err != nil {
		w.T.Fatal(err)
	}
	return n
}

func str(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func ok(a live.Action) string {
	if a.OK == nil {
		return "None"
	}
	return fmt.Sprint(*a.OK)
}

func plan(t *testing.T, raw *string) map[string]any {
	t.Helper()
	if raw == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func want[T any](t *testing.T, what string, got, expected T) {
	t.Helper()
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("%s = %#v, want %#v", what, got, expected)
	}
}

func TestEligibilityAndICloudMoveWithUndoData(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("read", arrival{})
	w.arrive("unread", arrival{flags: []string{}})
	w.arrive("flagged", arrival{flags: []string{seen, flagged}})
	w.arrive("undecided", arrival{})
	for _, s := range []string{"read", "unread", "flagged"} {
		w.decide(s, "Bills")
	}
	mid := w.MID("read")
	out := w.apply()
	if out.Written != 1 || out.Skipped != 0 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "inbox", w.Fake.Boxes["INBOX"].UIDs(), []uint32{2, 3, 4})
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{1})
	want(t, "verbs", w.verbs(), []string{"COPY", "STORE", "EXPUNGE"})
	want(t, "expunged", w.Fake.Writes[2].UIDs, []uint32{1})
	actions := w.Actions()
	if len(actions) != 1 {
		t.Fatalf("actions %+v", actions)
	}
	row := actions[0]
	want(t, "row", []string{row.Kind, ok(row), str(row.Dest), str(row.Src)}, []string{"file", "1", "Bills", "INBOX"})
	want(t, "after", str(row.After), `{"imap_name": "Bills", "uidvalidity": 3, "uid": 1}`)
	want(t, "undo", str(row.Undo), fmt.Sprintf(`{"op": "move_back", "account": "icloud", "src_imap": "INBOX", "dest_imap": "Bills", `+
		`"dest_uidvalidity": 3, "items": [{"id": %d, "msgid": "read@x.test", "uid": 1, "dest_uid": 1}]}`, mid))
	want(t, "before", str(row.Before), `{"imap_name": "INBOX", "uidvalidity": 1, "uid": 1, "msgid": "read@x.test", "flags": ["\\Seen"], "labels": []}`)
	if row.DecisionID == nil {
		t.Fatal("decision id not logged")
	}
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("second pass %+v", out)
	}
}

func TestMessagesEligibleBeforeGoLiveAreNotFiled(t *testing.T) {
	w := icloud(t, "live", livetest.Seed{Subject: "old", Flags: []string{seen}})
	w.arrive("new", arrival{})
	w.decide("old", "Bills")
	w.decide("new", "Bills")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "inbox", w.Fake.Boxes["INBOX"].UIDs(), []uint32{1})
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{1})
	want(t, "baseline", w.count("SELECT message_id FROM live_baseline"), int(w.MID("old")))
}

func TestGmailRelabelArchivesAndUncertainStays(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("filed", arrival{})
	w.arrive("uncertain", arrival{})
	w.decide("filed", "Receipts")
	w.Decide("uncertain", "none", nil, "fallback")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	msgs := w.Fake.Boxes[allMail].Messages
	want(t, "filed", msgs[1].Labels, []string{"Receipts"})
	want(t, "uncertain", msgs[2].Labels, []string{inboxLabel})
	want(t, "verbs", w.verbs(), []string{"STORE", "STORE"})
	actions := w.Actions()
	want(t, "undo", str(actions[0].Undo), `{"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "uids": [1], "add": ["\\Inbox"], "remove": ["Receipts"]}`)
	want(t, "after", str(actions[0].After), `{"add": ["Receipts"], "remove": ["\\Inbox"]}`)
}

func TestGmailLabelAlreadyPresentOnlyArchives(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("twice", arrival{labels: []string{inboxLabel, "Receipts"}})
	w.decide("twice", "Receipts")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{"Receipts"})
	want(t, "writes", len(w.Fake.Writes), 1)
}

func TestRoutingTimeDeleteDecisionTrashes(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("junk", arrival{})
	w.Decide("junk", "delete", nil, "operator")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "trash", w.Fake.Boxes["Deleted Messages"].UIDs(), []uint32{1})
	want(t, "inbox", len(w.Fake.Boxes["INBOX"].UIDs()), 0)
	actions := w.Actions()
	want(t, "kind", actions[0].Kind, "trash")
	want(t, "dest", str(actions[0].Dest), "Deleted Messages")
}

func TestContactsFlagSetsFlagNeverClearsAndUndoIsNotReapplied(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("friend", arrival{flags: []string{}})
	w.arrive("stranger", arrival{flags: []string{}})
	mid := w.MID("friend")
	w.flagDecision("friend", 1)
	w.flagDecision("stranger", 0)
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	inbox := w.Fake.Boxes["INBOX"].Messages
	if !slices.Contains(inbox[1].Flags, flagged) || slices.Contains(inbox[2].Flags, flagged) {
		t.Fatalf("flags %v %v", inbox[1].Flags, inbox[2].Flags)
	}
	want(t, "flagged column", w.count("SELECT flagged FROM messages WHERE id = ?", mid), 1)
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("second pass %+v", out)
	}
	actions := w.Actions()
	want(t, "kind", actions[0].Kind, "flag")
	want(t, "undo", str(actions[0].Undo), `{"op": "unflag", "account": "icloud", "imap_name": "INBOX", "uidvalidity": 1, "uids": [1]}`)
	w.undo(actions[0].ID)
	if slices.Contains(inbox[1].Flags, flagged) {
		t.Fatal("undo left the flag")
	}
	w.Sync()
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("pass after undo %+v", out)
	}
}

func TestFlagSkippedWhenMessageWasReadOnServer(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("friend", arrival{flags: []string{}})
	w.flagDecision("friend", 1)
	w.Fake.Boxes["INBOX"].SetFlags(1, []string{seen}, nil)
	out := w.apply()
	if out.Written != 0 || out.Skipped != 1 || len(w.Fake.Writes) != 0 {
		t.Fatalf("outcome %+v writes %v", out, w.Fake.Writes)
	}
	want(t, "error", str(w.Actions()[0].Error), "skipped: read")
}

func TestDeletionPassSpansFoldersAppliesAgeAndSkipsFlagged(t *testing.T) {
	w := icloud(t, "live")
	spam := "x@spam.test"
	w.arrive("in-old-unread", arrival{flags: []string{}, sender: spam})
	w.arrive("bills-old", arrival{box: "Bills", sender: spam})
	w.arrive("bills-keep", arrival{box: "Bills", sender: "x@fine.test"})
	w.arrive("fresh", arrival{sender: spam})
	w.arrive("old-flagged", arrival{flags: []string{seen, flagged}, sender: spam})
	w.arrive("sent-old", arrival{box: "Sent Messages", sender: spam})
	for _, s := range []string{"in-old-unread", "bills-old", "old-flagged", "sent-old"} {
		w.age(s, 1.0)
	}
	w.age("fresh", float64(time.Now().Unix()))
	w.deleteRule("")
	out := w.deletions(false)
	if !out.Ran || !reflect.DeepEqual(out.ByKind, py.Dict{{Key: "trash", Value: 2}}) {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "trash", w.Fake.Boxes["Deleted Messages"].UIDs(), []uint32{1, 2})
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{2})
	want(t, "inbox", w.Fake.Boxes["INBOX"].UIDs(), []uint32{2, 3})
	want(t, "sent", w.Fake.Boxes["Sent Messages"].UIDs(), []uint32{1})
	state, err := live.GetState(ctx, w.DB.Read, "deletion_pass_ts:icloud")
	if err != nil || state == nil || *state != py.FloatRepr(w.Clock) {
		t.Fatalf("deletion_pass_ts = %v %v", state, err)
	}
	if w.deletions(false).Ran {
		t.Fatal("deletion pass ran again within the interval")
	}
	w.Clock += config.DeletionIntervalSeconds
	if !w.deletions(false).Ran {
		t.Fatal("deletion pass not due after the interval")
	}
}

func TestDeletionCandidatesExcludeSystemRoles(t *testing.T) {
	w := icloud(t, "live")
	for i, role := range []string{"trash", "sent", "drafts", "junk", "system"} {
		id := 100 + i
		w.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, 'icloud', ?, ?, ?)", id, role, role, role)
		w.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, from_domain, date_ts, seen) "+
			"VALUES ('icloud', ?, 1, 1, ?, 0, 'spam.test', 1, 1)", id, fmt.Sprintf("sys%d", i))
	}
	w.deleteRule("")
	got, err := w.applier.deletionIntents(ctx, config.ICloud, &Budget{Left: 100})
	if err != nil || len(got) != 0 {
		t.Fatalf("deletion intents = %+v, %v", got, err)
	}
	w.Exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (200, 'icloud', 'Old', 'Old', 'filed')")
	w.Exec("INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, from_domain, date_ts, seen) " +
		"VALUES ('icloud', 200, 1, 1, 'filed', 0, 'spam.test', 1, 1)")
	got, err = w.applier.deletionIntents(ctx, config.ICloud, &Budget{Left: 100})
	if err != nil || len(got) != 1 || got[0].Digest != "filed" {
		t.Fatalf("deletion intents = %+v, %v", got, err)
	}
}

func TestGmailDeletionPassCopiesToTrashAndUndoUntrashes(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("doomed", arrival{labels: []string{"Receipts"}, sender: "x@spam.test"})
	w.age("doomed", 1.0)
	w.deleteRule(config.Gmail)
	out := w.deletions(false)
	want(t, "by kind", out.ByKind, py.Dict{{Key: "trash", Value: 1}})
	want(t, "verbs", w.verbs(), []string{"COPY"})
	row := w.Actions()[0]
	want(t, "undo", str(row.Undo), `{"op": "gmail_untrash", "trash_imap": "[Gmail]/Trash", "dest_uidvalidity": 5, "uids": [1]}`)
	if !w.undo(row.ID).Undone {
		t.Fatal("not undone")
	}
}

func TestWriteCapDefersTheExcessAndWarns(t *testing.T) {
	w := icloud(t, "live")
	w.applier.Cap = 2
	for i := range 5 {
		w.arrive(fmt.Sprintf("m%d", i), arrival{})
		w.decide(fmt.Sprintf("m%d", i), "Bills")
	}
	buf := captureLog(t)
	if out := w.apply(); out.Written != 2 {
		t.Fatalf("outcome %+v", out)
	}
	if !strings.Contains(buf.String(), "write cap reached: deferring 3 inbox decisions to a later pass") {
		t.Fatalf("log %q", buf.String())
	}
	if !strings.Contains(buf.String(), "icloud: live pass applied={'file': 2} skipped=0 failed=0 deferred=3") {
		t.Fatalf("log %q", buf.String())
	}
	want(t, "bills", len(w.Fake.Boxes["Bills"].Messages), 2)
	if out := w.apply(); out.Written != 2 {
		t.Fatalf("second pass %+v", out)
	}
}

func TestDeletionPassHonoursTheCap(t *testing.T) {
	w := icloud(t, "live")
	w.applier.Cap = 1
	for i := range 3 {
		w.arrive(fmt.Sprintf("d%d", i), arrival{sender: "x@spam.test"})
		w.age(fmt.Sprintf("d%d", i), 1.0)
	}
	w.deleteRule("")
	buf := captureLog(t)
	if out := w.deletions(false); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if !strings.Contains(buf.String(), "deferring 2 deletions") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestPauseSkipsWritesKeepsDecisionsAndResumeApplies(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("held", arrival{})
	w.decide("held", "Bills")
	if err := w.Live.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	if out := w.apply(); out.Ran || len(w.Fake.Writes) != 0 {
		t.Fatalf("paused pass %+v writes %v", out, w.Fake.Writes)
	}
	if !strings.Contains(buf.String(), `msg="icloud: paused, automatic writes skipped"`) || !strings.Contains(buf.String(), "level=INFO") {
		t.Fatalf("log %q", buf.String())
	}
	want(t, "decisions", w.count("SELECT COUNT(*) FROM decisions"), 1)
	if w.deletions(false).Ran {
		t.Fatal("deletion pass ran while paused")
	}
	if state, _ := live.GetState(ctx, w.DB.Read, "deletion_pass_ts:icloud"); state != nil {
		t.Fatal("a skipped deletion pass recorded its time")
	}
	if err := w.Live.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("resumed pass %+v", out)
	}
}

func TestShadowModeRefusesWrites(t *testing.T) {
	w := icloud(t, "shadow")
	w.arrive("quiet", arrival{})
	w.decide("quiet", "Bills")
	if w.apply().Ran || len(w.Fake.Writes) != 0 {
		t.Fatal("shadow mode wrote")
	}
	err := w.Fake.StoreFlags(ctx, mail.UIDs(1), true, flagged)
	if _, refused := errors.AsType[safety.WriteRefused](err); !refused {
		t.Fatalf("shadow store = %v", err)
	}
}

func TestUndoMovesBackAndIsNeverRefiled(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("oops", arrival{})
	w.decide("oops", "Bills")
	w.apply()
	row := w.Actions()[0]
	if !w.undo(row.ID).Undone {
		t.Fatal("not undone")
	}
	want(t, "bills", len(w.Fake.Boxes["Bills"].UIDs()), 0)
	want(t, "inbox", len(w.Fake.Boxes["INBOX"].Messages), 1)
	if w.Actions()[0].UndoneTS == nil {
		t.Fatal("undone_ts unset")
	}
	if _, err := w.Live.Undo(ctx, row.ID); !errors.As(err, new(live.UndoError)) {
		t.Fatalf("second undo = %v", err)
	}
	w.Sync()
	w.Exec("INSERT INTO decisions (message_id, account, ts, stage, key, action, dest) VALUES (?, 'icloud', 2, 'learned', 'k', 'file', 'Bills')", w.MID("oops"))
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("refiled %+v", out)
	}
}

func TestGmailUndoRestoresInboxLabelAndIsNotReapplied(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("g", arrival{})
	w.decide("g", "Receipts")
	w.apply()
	w.undo(w.Actions()[0].ID)
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{inboxLabel})
	w.Sync()
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("reapplied %+v", out)
	}
}

func TestServerSideChangeSinceDecisionIsSkipped(t *testing.T) {
	w := icloud(t, "live")
	for _, s := range []string{"flipped", "vanished"} {
		w.arrive(s, arrival{})
		w.decide(s, "Bills")
	}
	w.Fake.Boxes["INBOX"].SetFlags(1, []string{}, nil)
	w.Fake.Boxes["INBOX"].Remove(2)
	out := w.apply()
	if out.Written != 0 || out.Skipped != 2 || len(w.Fake.Writes) != 0 {
		t.Fatalf("outcome %+v writes %v", out, w.Fake.Writes)
	}
	var errs []string
	for _, a := range w.Actions() {
		errs = append(errs, str(a.Error))
		want(t, "ok", ok(a), "0")
	}
	slices.Sort(errs)
	want(t, "errors", errs, []string{"skipped: missing", "skipped: unread"})
}

func TestCrashAfterWriteDoesNotDoubleApply(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("crashy", arrival{})
	w.decide("crashy", "Bills")
	w.Exec("CREATE TRIGGER crash BEFORE UPDATE ON auto_actions BEGIN SELECT RAISE(ABORT, 'crash'); END")
	if _, err := w.applier.ApplyAccount(ctx, w.Account); err == nil || !strings.Contains(err.Error(), "crash") {
		t.Fatalf("crashed pass = %v", err)
	}
	w.Exec("DROP TRIGGER crash")
	want(t, "verbs", w.verbs(), []string{"COPY", "STORE", "EXPUNGE"})
	if a := w.Actions()[0]; a.OK != nil || a.Error != nil {
		t.Fatalf("crashed row %+v", a)
	}
	header, text := mailtest.RawWith("crashy", "a@shop.test", "crashy", "hello")
	w.Fake.Boxes["INBOX"].Add(header, text, []string{seen}, nil)
	w.Fake.Writes = nil
	if out := w.apply(); out.Written != 0 || len(w.Fake.Writes) != 0 {
		t.Fatalf("retried %+v %v", out, w.Fake.Writes)
	}
	want(t, "error", str(w.Actions()[0].Error), live.Interrupted)
}

func TestPartialMoveIsHeldNotRetried(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("half", arrival{})
	w.decide("half", "Bills")
	w.Fake.FailWrite = func(x mailtest.FakeWrite) error {
		if x.Verb == "EXPUNGE" {
			return errors.New("connection reset")
		}
		return nil
	}
	buf := captureLog(t)
	out := w.apply()
	if out.Failed != 1 || out.Written != 0 {
		t.Fatalf("outcome %+v", out)
	}
	row := w.Actions()[0]
	want(t, "ok", ok(row), "None")
	want(t, "error", str(row.Error), "copied but not expunged: connection reset")
	want(t, "after", str(row.After), `{"imap_name": "Bills", "uidvalidity": 3, "uid": 1}`)
	if !strings.Contains(buf.String(), "icloud: partial move from INBOX to Bills: copied but not expunged: connection reset") {
		t.Fatalf("log %q", buf.String())
	}
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("retried %+v", out)
	}
	copies := 0
	for _, v := range w.verbs() {
		if v == "COPY" {
			copies++
		}
	}
	want(t, "copies", copies, 1)
}

func TestFailedAttemptsAreLimited(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("stubborn", arrival{})
	w.decide("stubborn", "Nowhere")
	for range config.WriteAttempts + 2 {
		w.apply()
	}
	actions := w.Actions()
	want(t, "attempts", len(actions), config.WriteAttempts)
	for _, a := range actions {
		want(t, "error", str(a.Error), "skipped: no such folder")
		want(t, "src", str(a.Src), "INBOX")
	}
}

func TestCorrectionMovesICloudMessageEvenWhenUnreadAndUndoClearsIt(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("c1", arrival{flags: []string{}})
	mid := w.correct("c1", "Bills")
	out, err := w.applier.ApplyCorrections(ctx, config.ICloud, []int64{mid})
	if err != nil || out.Written != 1 {
		t.Fatalf("outcome %+v %v", out, err)
	}
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{1})
	row := w.Actions()[0]
	want(t, "kind", row.Kind, "correct")
	if row.DecisionID != nil {
		t.Fatal("a correction carries no decision id")
	}
	w.undo(row.ID)
	if c, err := corrections.Get(ctx, w.DB.Read, mid); err != nil || c != nil {
		t.Fatalf("correction = %+v %v", c, err)
	}
	want(t, "bills", len(w.Fake.Boxes["Bills"].UIDs()), 0)
}

func TestApplyCorrectionsOnlyMovesTheGivenMessages(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("mine", arrival{flags: []string{}})
	w.arrive("other", arrival{flags: []string{}})
	mid := w.correct("mine", "Bills")
	w.correct("other", "Misc")
	out, err := w.applier.ApplyCorrections(ctx, config.ICloud, []int64{mid})
	if err != nil || out.Written != 1 {
		t.Fatalf("outcome %+v %v", out, err)
	}
	want(t, "misc", len(w.Fake.Boxes["Misc"].UIDs()), 0)
}

func TestCorrectionToUnknownFolderIsSkipped(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("c2", arrival{})
	w.correct("c2", "Nowhere")
	out := w.apply()
	if out.Written != 0 || out.Skipped != 1 || len(w.Fake.Writes) != 0 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestCorrectionReplacesAtriumsGmailLabelAndStaysArchived(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("g2", arrival{labels: []string{"Receipts"}})
	mid := w.MID("g2")
	var digest string
	if err := w.DB.Read.QueryRow("SELECT digest FROM messages WHERE id = ?", mid).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	prior, _, err := w.Live.Begin(ctx, config.Gmail, mid, digest, live.File, new(allMail), new("Receipts"), py.Dict{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Live.Finish(ctx, prior, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	w.correct("g2", "Taxes")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{"Taxes"})
	w.Sync()
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("second pass %+v", out)
	}
}

func correctBeforeGoLive(w *world, subject string) int64 {
	w.T.Helper()
	mid := w.correct(subject, "Bills")
	w.Exec("UPDATE corrections SET ts = 1.0 WHERE message_id = ?", mid)
	return mid
}

func TestShadowEraCorrectionsAreNeverMovedAutomatically(t *testing.T) {
	w := icloud(t, "live",
		livetest.Seed{Subject: "read", Flags: []string{seen}},
		livetest.Seed{Subject: "unread", Flags: []string{}},
		livetest.Seed{Subject: "flagged", Flags: []string{seen, flagged}})
	for _, s := range []string{"read", "unread", "flagged"} {
		correctBeforeGoLive(w, s)
	}
	if out := w.apply(); out.Written != 0 || len(w.Fake.Writes) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	w.correct("flagged", "Bills")
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{1})
}

func TestPreGoLiveCorrectionFilesMailThatBecomesEligibleLater(t *testing.T) {
	w := icloud(t, "live", livetest.Seed{Subject: "later", Flags: []string{}})
	correctBeforeGoLive(w, "later")
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("outcome %+v", out)
	}
	w.Fake.Boxes["INBOX"].SetFlags(1, []string{seen}, nil)
	w.Sync()
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "bills", w.Fake.Boxes["Bills"].UIDs(), []uint32{1})
}

func TestDeletionPassReachesMailAtriumFiledButNotUndoneOrAlreadyTrashed(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("filed", arrival{sender: "x@spam.test"})
	w.decide("filed", "Bills")
	w.apply()
	w.Sync()
	w.age("filed", 1.0)
	w.deleteRule("")
	out := w.deletions(true)
	want(t, "by kind", out.ByKind, py.Dict{{Key: "trash", Value: 1}})
	want(t, "trash", w.Fake.Boxes["Deleted Messages"].UIDs(), []uint32{1})
	var trashed live.Action
	for _, a := range w.Actions() {
		if a.Kind == live.Trash {
			trashed = a
		}
	}
	want(t, "src", str(trashed.Src), "Bills")
	w.undo(trashed.ID)
	w.Sync()
	if out := w.deletions(true); out.Written != 0 {
		t.Fatalf("trashed again %+v", out)
	}
}

func TestGmailSelectFailureDoesNotStrandOtherActions(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("doomed", arrival{sender: "x@spam.test"})
	w.arrive("friend", arrival{flags: []string{}})
	w.Decide("doomed", "delete", nil, "operator")
	w.flagDecision("friend", 1)
	calls := 0
	w.client.selectRW = func(ctx context.Context, name string) error {
		calls++
		if calls == 1 {
			return errors.New("NO [SERVERBUG] try later")
		}
		return w.Fake.SelectRW(ctx, name)
	}
	out := w.apply()
	if out.Failed != 1 || out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	kinds := map[string]string{}
	for _, a := range w.Actions() {
		kinds[a.Kind] = ok(a)
		if a.Kind == live.Trash {
			want(t, "error", str(a.Error), "RuntimeError: NO [SERVERBUG] try later")
		}
	}
	want(t, "kinds", kinds, map[string]string{"trash": "0", "flag": "1"})
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("retry %+v", out)
	}
}

func refuseLabelRemoval(w *world, msg string) {
	w.Fake.FailWrite = func(x mailtest.FakeWrite) error {
		if x.Verb == "STORE" && x.Args[0] == "-X-GM-LABELS" {
			return errors.New(msg)
		}
		return nil
	}
}

func TestGmailPartialLabelStoreRecordsWhatTheServerHasAndUndoReversesIt(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("half", arrival{})
	w.decide("half", "Receipts")
	refuseLabelRemoval(w, "NO try later")
	out := w.apply()
	if out.Written != 0 || out.Failed != 1 {
		t.Fatalf("outcome %+v", out)
	}
	row := w.Actions()[0]
	want(t, "ok", ok(row), "0")
	want(t, "error", str(row.Error), "partial: RuntimeError: NO try later")
	want(t, "undo", str(row.Undo), `{"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "uids": [1], "add": [], "remove": ["Receipts"]}`)
	want(t, "after", str(row.After), `{"add": ["Receipts"], "remove": []}`)
	w.Fake.FailWrite = nil
	w.undo(row.ID)
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{inboxLabel})
}

func TestGmailTrashCopyThatFailedAfterCopyingStillGetsAnUndo(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("gone", arrival{sender: "x@spam.test"})
	w.Decide("gone", "delete", nil, "operator")
	w.client.copy = func(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
		if _, err := w.Fake.Copy(ctx, uids, dest); err != nil {
			return nil, err
		}
		return nil, errors.New("NO connection dropped")
	}
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("outcome %+v", out)
	}
	row := w.Actions()[0]
	want(t, "ok", ok(row), "1")
	want(t, "note", str(row.Error), "partial: RuntimeError: NO connection dropped")
	want(t, "undo", str(row.Undo), `{"op": "gmail_untrash", "trash_imap": "[Gmail]/Trash", "dest_uidvalidity": 5, "uids": [1]}`)
	w.client.copy = nil
	if !w.undo(row.ID).Undone {
		t.Fatal("not undone")
	}
}

func TestGmailPartialRelabelIsRetriedAndOneUndoReversesBothAttempts(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("stuck", arrival{})
	w.decide("stuck", "Receipts")
	refuseLabelRemoval(w, "NO try later")
	if out := w.apply(); out.Written != 0 || out.Failed != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{inboxLabel, "Receipts"})
	w.Sync()
	w.Fake.FailWrite = nil
	if out := w.apply(); out.Written != 1 {
		t.Fatalf("retry %+v", out)
	}
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{"Receipts"})
	rows := w.Actions()
	if ok(rows[0]) != "0" || ok(rows[1]) != "1" || rows[0].Undo != nil {
		t.Fatalf("rows %+v", rows)
	}
	want(t, "undo", str(rows[1].Undo), `{"op": "gmail_labels", "imap_name": "[Gmail]/All Mail", "uidvalidity": 1, "uids": [1], "add": ["\\Inbox"], "remove": ["Receipts"]}`)
	w.undo(rows[1].ID)
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{inboxLabel})
	w.Sync()
	if out := w.apply(); out.Written != 0 {
		t.Fatalf("reapplied %+v", out)
	}
}

func TestPartialRelabelStopsAfterTheAttemptLimitButStaysUndoable(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("stubborn", arrival{})
	w.decide("stubborn", "Receipts")
	refuseLabelRemoval(w, "NO")
	for range config.WriteAttempts + 2 {
		w.Sync()
		w.apply()
	}
	rows := w.Actions()
	want(t, "attempts", len(rows), config.WriteAttempts)
	var holders []live.Action
	for _, r := range rows {
		if r.Undo != nil {
			holders = append(holders, r)
		}
	}
	if len(holders) != 1 || !holders[0].Undoable {
		t.Fatalf("holders %+v", holders)
	}
	w.Fake.FailWrite = nil
	w.undo(holders[0].ID)
	want(t, "labels", w.Fake.Boxes[allMail].Messages[1].Labels, []string{inboxLabel})
}

func TestTrashLookupIgnoresADifferentMessageWithTheSameMessageID(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("dup", arrival{sender: "x@spam.test"})
	w.Decide("dup", "delete", nil, "operator")
	header, text := mailtest.RawWith("dup", "other@x.test", "dup", "different body")
	w.Fake.Boxes[gmailTrash].Add(header, text, nil, nil)
	w.Fake.FailWrite = func(x mailtest.FakeWrite) error {
		if x.Verb == "COPY" {
			return errors.New("NO nothing was copied")
		}
		return nil
	}
	out := w.apply()
	if out.Written != 0 || out.Failed != 1 {
		t.Fatalf("outcome %+v", out)
	}
	row := w.Actions()[0]
	if ok(row) != "0" || row.Undo != nil {
		t.Fatalf("row %+v", row)
	}
	want(t, "error", str(row.Error), "RuntimeError: NO nothing was copied")
}

func TestApplyAllRunsTheLivePassThenTheDueDeletionPass(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("file-me", arrival{})
	w.decide("file-me", "Bills")
	w.arrive("spam", arrival{sender: "x@spam.test", box: "Bills"})
	w.age("spam", 1.0)
	w.deleteRule("")
	applied, deleted, err := w.applier.ApplyAll(ctx, config.ICloud)
	if err != nil || applied.Written != 1 || deleted.Kind(live.Trash) != 1 {
		t.Fatalf("ApplyAll = %+v %+v %v", applied, deleted, err)
	}
	_, deleted, err = w.applier.ApplyAll(ctx, config.ICloud)
	if err != nil || deleted.Ran {
		t.Fatalf("second ApplyAll deletion = %+v %v", deleted, err)
	}
}

func TestModePauseApprovalMatrix(t *testing.T) {
	for _, mode := range []string{config.Shadow, config.Live} {
		for _, paused := range []bool{false, true} {
			for _, approved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/paused=%v/approved=%v", mode, paused, approved), func(t *testing.T) {
					w := icloud(t, mode)
					w.arrive("file-me", arrival{})
					w.decide("file-me", "Bills")
					w.arrive("friend", arrival{flags: []string{}})
					w.flagDecision("friend", 1)
					w.arrive("spam", arrival{sender: "x@spam.test", box: "Bills"})
					w.age("spam", 1.0)
					w.deleteRule("")
					if err := w.Live.SetPaused(paused); err != nil {
						t.Fatal(err)
					}
					caller := ctx
					if approved {
						caller = safety.ApprovedWrites(ctx, "p1")
					}
					automatic := mode == config.Live && !paused
					applied, err := w.applier.ApplyAccount(caller, config.ICloud)
					if err != nil {
						t.Fatal(err)
					}
					deleted, err := w.applier.DeletionPass(caller, config.ICloud, true)
					if err != nil {
						t.Fatal(err)
					}
					if applied.Ran != automatic || deleted.Ran != automatic || (len(w.Fake.Writes) > 0) != automatic {
						t.Fatalf("automatic writes %v %v %v, want %v", applied, deleted, w.Fake.Writes, automatic)
					}
					if automatic && (applied.Written != 2 || deleted.Written != 1) {
						t.Fatalf("automatic writes %+v %+v", applied, deleted)
					}
					for _, x := range w.Fake.Writes {
						if x.Verb == "STORE" && x.Args[0] == "-FLAGS.SILENT" && slices.Contains(x.Args, flagged) {
							t.Fatalf("an automatic path cleared a flag: %+v", x)
						}
					}
					w.Fake.Writes = nil
					err = mailops.StoreFlag(caller, w.Fake, "INBOX", []uint32{2}, seen, true)
					operator := approved || mode == config.Live
					if (err == nil) != operator || (len(w.Fake.Writes) > 0) != operator {
						t.Fatalf("operator write = %v %v, want %v", err, w.Fake.Writes, operator)
					}
				})
			}
		}
	}
}

func TestPauseStopsAutomaticWritesButNotApprovalsOrUndo(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("friend", arrival{flags: []string{}})
	w.flagDecision("friend", 1)
	w.apply()
	if err := w.Live.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	w.arrive("held", arrival{})
	w.decide("held", "Bills")
	w.Fake.Writes = nil
	if w.apply().Ran || w.deletions(true).Ran || len(w.Fake.Writes) != 0 {
		t.Fatal("a paused pass ran")
	}
	if !w.undo(w.Actions()[0].ID).Undone {
		t.Fatal("undo refused while paused")
	}
	approved := safety.ApprovedWrites(ctx, "42")
	if err := mailops.StoreFlag(approved, w.Fake, "INBOX", []uint32{2}, seen, true); err != nil {
		t.Fatalf("approved write while paused = %v", err)
	}
	want(t, "verbs", w.verbs(), []string{"STORE", "STORE"})
}

func TestPauseDuringAPassRefusesTheRemainingWrites(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("bills", arrival{})
	w.decide("bills", "Bills")
	w.arrive("misc", arrival{})
	w.decide("misc", "Misc")
	w.Fake.FailWrite = func(x mailtest.FakeWrite) error {
		if x.Verb == "COPY" {
			return w.Live.SetPaused(true)
		}
		return nil
	}
	out := w.apply()
	if out.Written != 0 || out.Failed != 2 {
		t.Fatalf("outcome %+v", out)
	}
	want(t, "verbs", w.verbs(), []string{"COPY"})
	rows := w.Actions()
	want(t, "held", []string{ok(rows[0]), str(rows[0].Error)}, []string{"None",
		"copied but not expunged: refusing automatic mailbox write 'IMAP STORE': atrium is paused"})
	want(t, "refused", []string{ok(rows[1]), str(rows[1].Error)}, []string{"0",
		"WriteRefused: refusing automatic mailbox write 'IMAP SELECT read-write': atrium is paused"})
}

func TestOnlyTheUnflagUndoClearsAFlag(t *testing.T) {
	clears := func(w *world) int {
		n := 0
		for _, x := range w.Fake.Writes {
			if x.Verb == "STORE" && x.Args[0] == "-FLAGS.SILENT" && slices.Contains(x.Args, flagged) {
				n++
			}
		}
		return n
	}
	w := icloud(t, "live")
	w.arrive("friend", arrival{flags: []string{}})
	w.flagDecision("friend", 1)
	w.arrive("starred", arrival{flags: []string{seen, flagged}, sender: "x@spam.test"})
	w.decide("starred", "Bills")
	w.arrive("file-me", arrival{})
	w.decide("file-me", "Bills")
	w.arrive("spam", arrival{sender: "x@spam.test", box: "Bills"})
	w.age("spam", 1.0)
	w.age("starred", 1.0)
	w.deleteRule("")
	w.arrive("fix", arrival{flags: []string{seen, flagged}})
	w.correct("fix", "Misc")
	w.apply()
	w.deletions(true)
	if clears(w) != 0 || len(w.Fake.Writes) == 0 {
		t.Fatalf("automatic writes %+v", w.Fake.Writes)
	}
	var flag live.Action
	for _, a := range w.Actions() {
		if a.Kind == live.Flag {
			flag = a
		}
	}
	w.undo(flag.ID)
	want(t, "flag clears", clears(w), 1)

	g := gmail(t, "live")
	g.arrive("friend", arrival{flags: []string{}})
	g.flagDecision("friend", 1)
	g.arrive("file-me", arrival{flags: []string{seen}})
	g.decide("file-me", "Receipts")
	g.apply()
	if clears(g) != 0 {
		t.Fatalf("gmail automatic writes %+v", g.Fake.Writes)
	}
}

func TestProtectedAndUnsafeDestinationsAreSkipped(t *testing.T) {
	w := gmail(t, "live")
	dests := []string{"Deleted Messages", "Sent Messages", "Trash", "[Gmail]/Spam", "a|b", `\Trash`, `\Spam`, `\Starred`, `\Important`, `\\Trash`, `Receipts\Q`}
	for i, dest := range dests {
		subject := fmt.Sprintf("d%d", i)
		w.arrive(subject, arrival{})
		w.decide(subject, dest)
	}
	out := w.apply()
	if out.Written != 1 || out.Skipped != 10 {
		t.Fatalf("outcome %+v", out)
	}
	for _, action := range w.Actions() {
		if str(action.Dest) != `Receipts\Q` {
			want(t, "error "+str(action.Dest), str(action.Error), "skipped: unsafe destination")
		}
	}
	var labelWrites int
	for _, write := range w.Fake.Writes {
		if write.Verb == "STORE" && slices.Contains(write.Args, "+X-GM-LABELS") {
			labelWrites++
		}
	}
	if labelWrites != 1 {
		t.Fatalf("label writes %+v", w.Fake.Writes)
	}
}

func TestNewResyncsTheAccountOnAFreshSession(t *testing.T) {
	w := icloud(t, "live")
	w.arrive("read", arrival{})
	mid := w.decide("read", "Bills")
	a := New(w.Live, w.Open, w.Syncer)
	opened := w.Opened
	out, err := a.ApplyAccount(ctx, config.ICloud)
	if err != nil || out.Written != 1 || w.Opened != opened+2 {
		t.Fatalf("outcome %+v %v, %d sessions", out, err, w.Opened-opened)
	}
	want(t, "gone", w.count("SELECT gone_at IS NOT NULL FROM messages WHERE id = ?", mid), 1)
	want(t, "bills", w.count("SELECT COUNT(*) FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE mb.name = 'Bills' AND m.gone_at IS NULL"), 1)
}

func TestGroupFailureHoldsEveryEntry(t *testing.T) {
	w := gmail(t, "live")
	w.arrive("first", arrival{})
	w.arrive("second", arrival{})
	w.decide("first", "Receipts")
	w.decide("second", "Receipts")
	mid := w.MID("first")
	w.Exec("INSERT INTO auto_actions (ts, account, message_id, digest, kind, ok, error, undo) "+
		"SELECT 1, 'gmail', id, digest || '-old', 'file', 0, 'partial: earlier', 'not json' FROM messages WHERE id = ?", mid)
	buf := captureLog(t)
	out := w.apply()
	if out.Written != 0 || !strings.Contains(buf.String(), "gmail: group failed unexpectedly") {
		t.Fatalf("outcome %+v log %q", out, buf.String())
	}
	held := w.Actions()[1:]
	if len(held) != 2 || held[0].Error == nil || ok(held[0]) != "None" || ok(held[1]) != "None" || str(held[1].Error) != str(held[0].Error) {
		t.Fatalf("held rows %+v", held)
	}
}

func TestDefaultCapIsWriteCap(t *testing.T) {
	if (&Applier{}).cap() != config.WriteCap || config.WriteCap != 100 || config.WriteAttempts != 3 {
		t.Fatal("WRITE_CAP is 100 and WRITE_ATTEMPTS 3")
	}
}
