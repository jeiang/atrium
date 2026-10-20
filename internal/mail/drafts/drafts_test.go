package drafts_test

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/mail/smtp/smtptest"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var creds = map[string]string{
	"ICLOUD_MAIL_USERNAME": "me",
	"ICLOUD_APP_PASSWORD":  "pw",
	"GMAIL_ADDRESS":        "me@gmail.test",
	"GMAIL_APP_PASSWORD":   "pw",
}

type world struct {
	t      *testing.T
	db     *db.DB
	store  *drafts.Store
	smtp   *smtptest.Server
	imap   *mailtest.FakeClient
	cache  string
	clock  float64
	opened int
}

func newWorldOn(t *testing.T, d *db.DB, credentials map[string]string) *world {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache")}
	for k, v := range credentials {
		env[k] = v
	}
	cfg, err := config.Load(config.Map(env))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, db: d, smtp: &smtptest.Server{}, imap: mailtest.NewFakeClient(config.Map(env)), cache: cfg.CacheDir, clock: 1000}
	w.imap.AddBox("INBOX", mailtest.NewBox(1))
	w.imap.AddBox("Sent Messages", mailtest.NewBox(1), `\Sent`)
	w.store = &drafts.Store{
		DB:  d,
		Cfg: cfg,
		Open: func(ctx context.Context, account string) (mail.Client, error) {
			w.opened++
			if err := w.imap.Connect(ctx); err != nil {
				return nil, err
			}
			return w.imap, nil
		},
		SMTP: w.smtp.Factory,
		Now: func() float64 {
			w.clock++
			return w.clock
		},
	}
	return w
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := newWorldOn(t, dbtest.Schema(t), creds)
	for _, account := range []string{config.ICloud, config.Gmail} {
		w.exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", account, account)
	}
	w.exec("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'box', 'box', 'filed')")
	return w
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Write.Exec(query, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) message(uid int, fields map[string]any) int64 {
	w.t.Helper()
	cols := []string{"account", "mailbox_id", "uidvalidity", "uid", "digest", "first_seen"}
	args := []any{"icloud", 1, 1, uid, "d", 0.0}
	for k, v := range fields {
		cols = append(cols, k)
		args = append(args, v)
	}
	res, err := w.db.Write.Exec("INSERT INTO messages ("+strings.Join(cols, ", ")+") VALUES (?"+strings.Repeat(", ?", len(cols)-1)+")", args...)
	if err != nil {
		w.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (w *world) original() int64 {
	return w.message(1, map[string]any{"from_addr": "friend@x.test", "from_name": "Friend", "subject": "Lunch?", "msgid": "<orig@x.test>", "to_addrs": `["me@icloud.test"]`})
}

func (w *world) get(id int64) drafts.Row {
	w.t.Helper()
	row, err := w.store.Get(context.Background(), id)
	if err != nil || row == nil {
		w.t.Fatalf("get %d: %v %v", id, row, err)
	}
	return *row
}

func (w *world) create(account string, to, cc any, subject, body string, replyTo *int64) int64 {
	w.t.Helper()
	var reply any
	if replyTo != nil {
		reply = *replyTo
	}
	id, err := w.store.Create(context.Background(), new(int64(1)), account, to, cc, subject, body, reply)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func draftError(t *testing.T, err error, want string) {
	t.Helper()
	var derr drafts.Error
	if !errors.As(err, &derr) || derr.Error() != want {
		t.Fatalf("got %v (%T), want DraftError(%q)", err, err, want)
	}
}

func TestDraftCreateThreadsTheReplyAndNeverSends(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	mid := w.original()
	id := w.create(config.ICloud, []any{"friend@x.test"}, nil, "Re: Lunch?", "Yes", &mid)
	row := w.get(id)
	if row.Status != drafts.Draft || row.InReplyTo != "<orig@x.test>" || row.Refs != "<orig@x.test>" {
		t.Fatalf("row %+v", row)
	}
	if len(w.smtp.Calls) != 0 {
		t.Fatalf("SMTP used: %+v", w.smtp.Calls)
	}
	updated, err := w.store.Update(ctx, id, map[string]any{"body": "Yes please"})
	if err != nil || updated.Body != "Yes please" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	_, err = w.store.Create(ctx, nil, config.ICloud, []any{"a@x.test\r\nBcc: z@x.test"}, nil, "s", "b", nil)
	draftError(t, err, "to must be a single line")
}

func TestCreateAndUpdateValidation(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	for _, c := range []struct {
		account string
		to, cc  any
		subject string
		body    any
		want    string
	}{
		{"yahoo", "a@x.test", nil, "s", "b", "account must be one of icloud, gmail"},
		{"icloud", nil, nil, "s", "b", "to is required"},
		{"icloud", []any{}, nil, "s", "b", "to is required"},
		{"icloud", []any{"a@x.test", 5}, nil, "s", "b", "to must be a list of addresses"},
		{"icloud", "a@x.test", "not an address", "s", "b", "cc has an invalid address"},
		{"icloud", "a@x.test", nil, "line\nbreak", "b", "subject must be a single line"},
		{"icloud", "a@x.test", nil, strings.Repeat("s", 301), "b", "subject is limited to 300 characters"},
		{"icloud", "a@x.test", nil, "s", "  \n", "body is required"},
		{"icloud", "a@x.test", nil, "s", nil, "body is required"},
		{"icloud", "a@x.test", nil, "s", strings.Repeat("b", drafts.MaxBody+1), "body is limited to 100000 characters"},
	} {
		_, err := w.store.Create(ctx, nil, c.account, c.to, c.cc, c.subject, c.body, nil)
		draftError(t, err, c.want)
	}
	_, err := w.store.Create(ctx, nil, config.ICloud, "a@x.test", nil, "s", "b", int64(999))
	draftError(t, err, "no message 999")

	id := w.create(config.ICloud, "a@x.test", nil, "s", "b", nil)
	for _, c := range []struct {
		changes map[string]any
		want    string
	}{
		{map[string]any{}, "nothing to change"},
		{map[string]any{"account": "yahoo"}, "account must be one of icloud, gmail"},
		{map[string]any{"to": ""}, "to is required"},
		{map[string]any{"body": ""}, "body must be 1..100000 characters"},
		{map[string]any{"body": 5}, "body must be 1..100000 characters"},
	} {
		_, err := w.store.Update(ctx, id, c.changes)
		draftError(t, err, c.want)
	}
	_, err = w.store.Update(ctx, 999, map[string]any{"body": "x"})
	draftError(t, err, "no draft 999")
	if err := w.store.Discard(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = w.store.Update(ctx, id, map[string]any{"body": "x"})
	draftError(t, err, "draft is discarded, only drafts can be edited")
	draftError(t, w.store.Discard(ctx, id), "only an unsent draft can be discarded")
}

func TestSendRequiresTheReviewedVersion(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	id := w.create(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	stale := w.get(id).UpdatedAt
	if _, err := w.store.Update(ctx, id, map[string]any{"to": []any{"attacker@x.test"}}); err != nil {
		t.Fatal(err)
	}
	_, err := w.store.Send(ctx, id, stale)
	draftError(t, err, "draft changed, review again")
	if len(w.smtp.Calls) != 0 || w.get(id).Status != drafts.Draft {
		t.Fatalf("stale send went out: %+v", w.smtp.Calls)
	}
	if _, err := w.store.Send(ctx, id, w.get(id).UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if got := w.smtp.Sent[0].To; !reflect.DeepEqual(got, []string{"attacker@x.test"}) {
		t.Fatalf("sent to %v", got)
	}
	_, err = w.store.Send(ctx, 999, 0)
	draftError(t, err, "draft is not in a sendable state")
}

func TestSendICloudUsesSMTPAndAppendsSent(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	mid := w.original()
	id := w.create(config.ICloud, []any{"friend@x.test"}, []any{"boss@x.test"}, "Re: Lunch?", "Yes", &mid)
	result, err := w.store.Send(ctx, id, w.get(id).UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Filed != "copy filed in Sent" || len(result.Refused) != 0 {
		t.Fatalf("result %+v", result)
	}
	sent := w.smtp.Sent[0]
	if sent.Host != "smtp.mail.me.com" || sent.From != "me@icloud.com" || !reflect.DeepEqual(sent.To, []string{"friend@x.test", "boss@x.test"}) {
		t.Fatalf("sent %+v", sent)
	}
	if sent.Msg.Get("In-Reply-To") != "<orig@x.test>" || sent.Msg.Get("References") != "<orig@x.test>" {
		t.Fatalf("threading headers %q %q", sent.Msg.Get("In-Reply-To"), sent.Msg.Get("References"))
	}
	wantCalls := []smtptest.Call{
		{Call: "connect", Host: "smtp.mail.me.com", Port: 587, Timeout: 60},
		{Call: "starttls"},
		{Call: "login", User: "me", Password: "pw"},
		{Call: "send_message", From: "me@icloud.com", To: []string{"friend@x.test", "boss@x.test"}},
		{Call: "quit"},
	}
	if !reflect.DeepEqual(w.smtp.Calls, wantCalls) {
		t.Fatalf("calls %+v", w.smtp.Calls)
	}
	if len(w.imap.Appended) != 1 || w.imap.Appended[0].Mailbox != "Sent Messages" || !reflect.DeepEqual(w.imap.Appended[0].Flags, []string{`\Seen`}) || !w.imap.Closed {
		t.Fatalf("appended %+v closed %v", w.imap.Appended, w.imap.Closed)
	}
	raw, err := sent.Msg.Bytes("\r\n", false)
	if err != nil || !bytes.Equal(w.imap.Appended[0].Raw, raw) {
		t.Fatalf("appended copy differs from the sent message: %v", err)
	}
	row := w.get(id)
	if row.Status != drafts.Sent || row.SentAt == nil || *row.SMTPResult != "sent; copy filed in Sent" {
		t.Fatalf("row %+v", row)
	}
	if want := strings.Trim(sent.Msg.Get("Message-ID"), "<>"); row.SentMsgID == nil || *row.SentMsgID != want {
		t.Fatalf("sent_msgid %v, want %s", row.SentMsgID, want)
	}
	_, err = w.store.Send(ctx, id, row.UpdatedAt)
	draftError(t, err, "draft is not in a sendable state")
	if len(w.smtp.Sent) != 1 {
		t.Fatalf("sent %d times", len(w.smtp.Sent))
	}
}

func TestSendGmailDoesNotAppend(t *testing.T) {
	w := newWorld(t)
	id := w.create(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	result, err := w.store.Send(t.Context(), id, w.get(id).UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if w.smtp.Sent[0].Host != "smtp.gmail.com" || w.smtp.Sent[0].From != "me@gmail.test" {
		t.Fatalf("sent %+v", w.smtp.Sent[0])
	}
	if result.Filed != "gmail files Sent itself" || w.opened != 0 || len(w.imap.Writes) != 0 {
		t.Fatalf("filed %q opened %d writes %+v", result.Filed, w.opened, w.imap.Writes)
	}
}

func TestSMTPFailureRevertsToDraft(t *testing.T) {
	w := newWorld(t)
	w.smtp.Fail = func(stage string) error {
		if stage == "login" {
			return &smtp.ResponseError{Kind: "SMTPAuthenticationError", Code: 535, Msg: "bad"}
		}
		return nil
	}
	id := w.create(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	_, err := w.store.Send(t.Context(), id, w.get(id).UpdatedAt)
	var serr smtp.SendError
	if !errors.As(err, &serr) || serr.Error() != "(535, b'bad')" {
		t.Fatalf("got %v (%T)", err, err)
	}
	row := w.get(id)
	if row.Status != drafts.Draft || *row.SMTPResult != "send failed: (535, b'bad')" || row.SentMsgID != nil {
		t.Fatalf("row %+v", row)
	}
}

func TestAppendGateOnlyInOperatorSendAndOnlyToSent(t *testing.T) {
	ctx := t.Context()
	shadow := config.Map(map[string]string{})
	refused := func(err error) bool {
		var r safety.WriteRefused
		return errors.As(err, &r)
	}
	if !refused(safety.WriteGate(ctx, shadow, safety.AppendAction, "Sent Messages")) {
		t.Fatal("APPEND outside operator send was allowed")
	}
	scoped := safety.OperatorSend(ctx, "Sent Messages")
	if err := safety.WriteGate(scoped, shadow, safety.AppendAction, "Sent Messages"); err != nil {
		t.Fatal(err)
	}
	if !refused(safety.WriteGate(scoped, shadow, safety.AppendAction, "INBOX")) {
		t.Fatal("APPEND to INBOX allowed in operator send")
	}
	if !refused(safety.WriteGate(scoped, shadow, mail.ActionStore, "Sent Messages")) {
		t.Fatal("STORE allowed in operator send")
	}
}

func TestOperatorSendAppendsOnlyToTheDiscoveredSentMailbox(t *testing.T) {
	for _, c := range []struct {
		name  string
		boxes [][2]string
		want  string
	}{
		{"special-use flag", [][2]string{{"INBOX", ""}, {"Archive", ""}, {"Drafts", `\Drafts`}, {"Gesendet", `\Sent`}, {"Sent Items", ""}}, "Gesendet"},
		{"name", [][2]string{{"INBOX", ""}, {"Notes", ""}, {"Sent Items", ""}}, "Sent Items"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.imap = mailtest.NewFakeClient(config.Map(map[string]string{}))
			for _, b := range c.boxes {
				var flags []string
				if b[1] != "" {
					flags = []string{b[1]}
				}
				w.imap.AddBox(b[0], mailtest.NewBox(1), flags...)
			}
			id := w.create(config.ICloud, "a@x.test", nil, "Hi", "body", nil)
			if _, err := w.store.Send(t.Context(), id, w.get(id).UpdatedAt); err != nil {
				t.Fatal(err)
			}
			want := []mailtest.FakeWrite{{Verb: "APPEND", Mailbox: c.want, Args: []string{`\Seen`}}}
			if !reflect.DeepEqual(w.imap.Writes, want) {
				t.Fatalf("writes %+v", w.imap.Writes)
			}
		})
	}
}

func TestSentCopyFailureNeverFailsTheSend(t *testing.T) {
	w := newWorld(t)
	w.store.Open = func(context.Context, string) (mail.Client, error) {
		return nil, errors.New("missing credentials for icloud")
	}
	id := w.create(config.ICloud, "a@x.test", nil, "Hi", "body", nil)
	result, err := w.store.Send(t.Context(), id, w.get(id).UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	row := w.get(id)
	if result.Filed != "Sent copy failed: missing credentials for icloud" || row.Status != drafts.Sent ||
		*row.SMTPResult != "sent; Sent copy failed: missing credentials for icloud" {
		t.Fatalf("result %+v row %+v", result, row)
	}
}

func TestUnexpectedRecipientsAndDiscard(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	mid := w.original()
	id := w.create(config.ICloud, []any{"friend@x.test", "stranger@evil.test"}, nil, "Re", "Yes", &mid)
	got, err := w.store.UnexpectedRecipients(ctx, w.get(id))
	if err != nil || !reflect.DeepEqual(got, []string{"stranger@evil.test"}) {
		t.Fatalf("unexpected %v %v", got, err)
	}
	fresh := w.create(config.ICloud, "stranger@evil.test", nil, "Hi", "x", nil)
	if got, err := w.store.UnexpectedRecipients(ctx, w.get(fresh)); err != nil || len(got) != 0 {
		t.Fatalf("non-reply %v %v", got, err)
	}
	if err := w.store.Discard(ctx, id); err != nil {
		t.Fatal(err)
	}
	if w.get(id).Status != drafts.Discarded {
		t.Fatal("not discarded")
	}
}

func TestEditFormUpdatesDraft(t *testing.T) {
	w := newWorld(t)
	id := w.create(config.ICloud, "a@x.test", nil, "Hi", "body", nil)
	row, err := w.store.Update(t.Context(), id, map[string]any{"account": "gmail", "to": "b@x.test", "cc": "", "subject": "S", "body": "B"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Account != "gmail" || row.ToAddrs != "b@x.test" || row.Subject != "S" || row.Body != "B" {
		t.Fatalf("row %+v", row)
	}
}

func TestReplyContextReadsTheCachedOriginal(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	raw := "From: Friend <friend@x.test>\r\nTo: me@icloud.test, Pal <pal@x.test>\r\nCc: =?utf-8?q?Zo=C3=AB?= <zoe@x.test>\r\n" +
		"Reply-To: list@x.test\r\nMessage-ID: <orig@x.test>\r\nReferences: <root@x.test>\r\n <mid@x.test>\r\nSubject: Lunch?\r\n\r\nbody\r\n"
	if err := os.MkdirAll(filepath.Join(w.cache, "messages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.cache, "messages", "orig.eml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	mid := w.message(1, map[string]any{"from_addr": "friend@x.test", "msgid": "", "raw_path": "messages/orig.eml"})
	id := w.create(config.ICloud, []any{"Friend@X.test", "pal@x.test", "zoe@x.test", "list@x.test", "new@x.test"}, nil, "Re: Lunch?", "Yes", &mid)
	row := w.get(id)
	if row.InReplyTo != "<orig@x.test>" || row.Refs != "<root@x.test> <mid@x.test> <orig@x.test>" {
		t.Fatalf("threading %q %q", row.InReplyTo, row.Refs)
	}
	got, err := w.store.UnexpectedRecipients(ctx, row)
	if err != nil || !reflect.DeepEqual(got, []string{"new@x.test"}) {
		t.Fatalf("unexpected %v %v", got, err)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(w.cache), "outside.eml"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := w.message(2, map[string]any{"from_addr": "friend@x.test", "from_name": "Friend", "msgid": "<o2@x.test>", "raw_path": "../outside.eml"})
	id = w.create(config.ICloud, "friend@x.test", nil, "Re", "Yes", &outside)
	if row := w.get(id); row.Refs != "<o2@x.test>" {
		t.Fatalf("a raw path outside the cache was read: %q", row.Refs)
	}
}

func insertSending(w *world, updatedAt float64) int64 {
	w.t.Helper()
	res, err := w.db.Write.Exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, created_at, updated_at) "+
		"VALUES (NULL, 'gmail', 'a@x.test', 's', 'b', 'sending', ?, ?)", updatedAt, updatedAt)
	if err != nil {
		w.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestStuckSendingDraftsBecomeUnknownNeverResent(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	old := insertSending(w, 1000)
	fresh := insertSending(w, 1500)
	w.store.Now = func() float64 { return 1700 }
	n, err := w.store.RecoverStuck(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	if w.get(old).Status != drafts.Unknown || w.get(fresh).Status != drafts.Sending {
		t.Fatalf("statuses %s %s", w.get(old).Status, w.get(fresh).Status)
	}
	_, err = w.store.Send(ctx, old, 1000)
	draftError(t, err, "draft is not in a sendable state")
	if err := w.store.ResolveUnknown(ctx, old, drafts.Draft); err != nil {
		t.Fatal(err)
	}
	if row := w.get(old); row.Status != drafts.Draft || *row.SMTPResult != "returned to draft by operator" {
		t.Fatalf("row %+v", row)
	}
	draftError(t, w.store.ResolveUnknown(ctx, old, drafts.Sent), "draft is not in the unknown state")
	if len(w.smtp.Calls) != 0 {
		t.Fatalf("SMTP used: %+v", w.smtp.Calls)
	}
}

func TestResolveUnknownAsSent(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	id := insertSending(w, 1000)
	w.store.Now = func() float64 { return 5000 }
	if _, err := w.store.RecoverStuck(ctx); err != nil {
		t.Fatal(err)
	}
	draftError(t, w.store.ResolveUnknown(ctx, id, "bogus"), "an unknown draft can only be marked sent or returned to draft")
	if err := w.store.ResolveUnknown(ctx, id, drafts.Sent); err != nil {
		t.Fatal(err)
	}
	row := w.get(id)
	if row.Status != drafts.Sent || row.SentAt == nil || *row.SentAt != 5000 || *row.SMTPResult != "marked sent by operator" {
		t.Fatalf("row %+v", row)
	}
	draftError(t, w.store.ResolveUnknown(ctx, id, drafts.Sent), "draft is not in the unknown state")
}

func TestDegradedStuckDraftOnGoldenDB(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	w := newWorldOn(t, d, creds)
	ctx := t.Context()
	const start = 1_800_000_000.0
	before, err := w.store.List(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	stuck := insertSending(w, start-drafts.SendingStaleSeconds-1)
	recent := insertSending(w, start-drafts.SendingStaleSeconds+1)
	w.store.Now = func() float64 { return start }
	if n, err := w.store.RecoverStuck(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	row := w.get(stuck)
	if row.Status != drafts.Unknown || *row.SMTPResult != "send interrupted; outcome unknown" || row.UpdatedAt != start {
		t.Fatalf("stuck row %+v", row)
	}
	if w.get(recent).Status != drafts.Sending {
		t.Fatal("a recent send was marked unknown")
	}
	_, err = w.store.Send(ctx, stuck, row.UpdatedAt)
	draftError(t, err, "draft is not in a sendable state")
	if len(w.smtp.Calls) != 0 {
		t.Fatalf("an unknown draft was resent: %+v", w.smtp.Calls)
	}
	for _, b := range before {
		if got := w.get(b.ID); !reflect.DeepEqual(got, b) {
			t.Fatalf("golden draft %d changed: %+v", b.ID, got)
		}
	}
}

func TestUpdateTypeErrorsFollowPython(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	mid := w.original()
	id := w.create(config.ICloud, "a@x.test", nil, "s", "b", nil)
	for _, c := range []struct {
		changes     map[string]any
		class, text string
	}{
		{map[string]any{"subject": int64(5)}, "AttributeError", "'int' object has no attribute 'strip'"},
		{map[string]any{"subject": []any{"x"}}, "AttributeError", "'list' object has no attribute 'strip'"},
		{map[string]any{"subject": true}, "AttributeError", "'bool' object has no attribute 'strip'"},
		{map[string]any{"in_reply_to_message_id": []any{}}, "ProgrammingError", "Error binding parameter 1: type 'list' is not supported"},
		{map[string]any{"in_reply_to_message_id": py.Dict{{Key: "a", Value: int64(1)}}}, "ProgrammingError", "Error binding parameter 1: type 'dict' is not supported"},
		{map[string]any{"in_reply_to_message_id": big.NewInt(1).Lsh(big.NewInt(1), 70)}, "OverflowError", "Python int too large to convert to SQLite INTEGER"},
	} {
		_, err := w.store.Update(ctx, id, c.changes)
		var classed py.Classed
		if !errors.As(err, &classed) || classed.PyClass() != c.class || err.Error() != c.text {
			t.Errorf("%v: err = %v (%T)", c.changes, err, err)
		}
	}
	if _, err := w.store.Update(ctx, id, map[string]any{"subject": int64(0)}); err != nil {
		t.Errorf("a falsy non-string subject is the empty string: %v", err)
	}
	for _, v := range []any{true, float64(mid), strconv.FormatInt(mid, 10), " " + strconv.FormatInt(mid, 10)} {
		if _, err := w.store.Update(ctx, id, map[string]any{"in_reply_to_message_id": v}); err != nil && v != true {
			t.Errorf("reply %v (%T): %v", v, v, err)
		}
	}
	got := w.get(id)
	if got.InReplyToMessageID == nil || *got.InReplyToMessageID != mid {
		t.Errorf("stored reply id = %v, want %d", got.InReplyToMessageID, mid)
	}
	for _, v := range []any{strconv.FormatInt(mid, 10) + ".0", strconv.FormatInt(mid, 10) + "e0", " " + strconv.FormatInt(mid, 10) + " "} {
		if _, err := w.store.Update(ctx, id, map[string]any{"in_reply_to_message_id": v}); err != nil {
			t.Errorf("reply %q: %v", v, err)
		}
	}
	draftError(t, func() error {
		_, err := w.store.Update(ctx, id, map[string]any{"in_reply_to_message_id": "\u00a0" + strconv.FormatInt(mid, 10)})
		return err
	}(), "no message \u00a0"+strconv.FormatInt(mid, 10))
	for v, want := range map[any]string{float64(1.5): "no message 1.5", "abc": "no message abc", false: "no message False", float64(999): "no message 999.0", "999": "no message 999"} {
		_, err := w.store.Update(ctx, id, map[string]any{"in_reply_to_message_id": v})
		draftError(t, err, want)
	}
}

func TestCreateValidatesBeforeTheReplyIDOverflows(t *testing.T) {
	w := newWorld(t)
	huge := new(big.Int).Lsh(big.NewInt(1), 70)
	_, err := w.store.Create(t.Context(), nil, "yahoo", "a@x.test", nil, "s", "b", huge)
	draftError(t, err, "account must be one of icloud, gmail")
	_, err = w.store.Create(t.Context(), nil, config.ICloud, "a@x.test", nil, "s", "b", huge)
	var classed py.Classed
	if !errors.As(err, &classed) || classed.PyClass() != "OverflowError" {
		t.Errorf("err = %v", err)
	}
	var n int
	if err := w.db.Read.QueryRow("SELECT COUNT(*) FROM drafts").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d drafts stored", n)
	}
}

func TestForMessage(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	id := w.create(config.Gmail, "a@x.test", nil, "Hi", "body", nil)
	if _, err := w.store.Send(ctx, id, w.get(id).UpdatedAt); err != nil {
		t.Fatal(err)
	}
	msgid := *w.get(id).SentMsgID
	copyID := w.message(5, map[string]any{"msgid": msgid})
	got, err := w.store.ForMessage(ctx, copyID)
	if err != nil || got == nil || got.ID != id {
		t.Fatalf("for_message %+v %v", got, err)
	}
	w.exec("UPDATE drafts SET thread_id = NULL WHERE id = ?", id)
	if got, err := w.store.ForMessage(ctx, copyID); err != nil || got != nil {
		t.Fatalf("draft without a thread matched: %+v %v", got, err)
	}
}
