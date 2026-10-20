package mailtest

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	gmailAllMail = "[Gmail]/All Mail"
	inboxLabel   = `\Inbox`
)

func env(mode string) config.Env {
	if mode == "" {
		return config.Map(map[string]string{})
	}
	return config.Map(map[string]string{"ATRIUM_MODE": mode})
}

func refused(err error) bool {
	var r safety.WriteRefused
	return errors.As(err, &r)
}

func icloud(mode string) *FakeClient {
	f := NewFakeClient(env(mode))
	inbox := f.AddBox("INBOX", NewBox(1))
	f.AddBox("Receipts", NewBox(5))
	f.AddBox("Sent Messages", NewBox(1), `\Sent`)
	h, b := Raw("quiet")
	inbox.Add(h, b, []string{`\Seen`}, nil)
	return f
}

var writes = []struct {
	name string
	do   func(context.Context, *FakeClient) error
}{
	{"select read-write", func(ctx context.Context, f *FakeClient) error { _, err := f.SelectRW(ctx, "INBOX"); return err }},
	{"copy", func(ctx context.Context, f *FakeClient) error {
		_, err := f.Copy(ctx, mail.UIDs(1), "Receipts")
		return err
	}},
	{"store flags", func(ctx context.Context, f *FakeClient) error {
		return f.StoreFlags(ctx, mail.UIDs(1), true, `\Flagged`)
	}},
	{"store labels", func(ctx context.Context, f *FakeClient) error {
		return f.StoreLabels(ctx, mail.UIDs(1), true, "Receipts")
	}},
	{"expunge", func(ctx context.Context, f *FakeClient) error { return f.UIDExpunge(ctx, mail.UIDs(1)) }},
	{"append", func(ctx context.Context, f *FakeClient) error {
		return f.Append(ctx, "Sent Messages", []byte("raw"), `\Seen`)
	}},
	{"create", func(ctx context.Context, f *FakeClient) error { return f.CreateMailbox(ctx, "New Box") }},
	{"subscribe", func(ctx context.Context, f *FakeClient) error { return f.SubscribeMailbox(ctx, "Receipts") }},
	{"delete", func(ctx context.Context, f *FakeClient) error { return f.DeleteMailbox(ctx, "Receipts") }},
}

func unchanged(t *testing.T, f *FakeClient) {
	t.Helper()
	if len(f.Writes) != 0 || len(f.Created) != 0 || len(f.Deleted) != 0 || len(f.Appended) != 0 || f.RW != "" {
		t.Fatalf("refused write changed the fake: writes %v created %v deleted %v appended %d rw %q",
			f.Writes, f.Created, f.Deleted, len(f.Appended), f.RW)
	}
	if got := f.Boxes["INBOX"].Messages[1].Flags; !reflect.DeepEqual(got, []string{`\Seen`}) {
		t.Fatalf("INBOX uid 1 flags = %v", got)
	}
	if !reflect.DeepEqual(f.Order, []string{"INBOX", "Receipts", "Sent Messages"}) || len(f.Boxes["Receipts"].Messages) != 0 {
		t.Fatalf("mailboxes changed: %v", f.Order)
	}
}

func TestFakeRefusesEveryWriteOutsideLive(t *testing.T) {
	for _, mode := range []string{"shadow", ""} {
		for _, w := range writes {
			t.Run(mode+"/"+w.name, func(t *testing.T) {
				f := icloud(mode)
				if err := f.Select(context.Background(), "INBOX"); err != nil {
					t.Fatal(err)
				}
				if err := w.do(context.Background(), f); !refused(err) {
					t.Fatalf("err = %v, want WriteRefused", err)
				}
				unchanged(t, f)
			})
		}
	}
}

func TestFakeRefusesAutomaticWritesWhilePaused(t *testing.T) {
	pause := filepath.Join(t.TempDir(), "pause")
	if err := safety.SetPaused(pause, true); err != nil {
		t.Fatal(err)
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			f := icloud("live")
			if err := f.Select(context.Background(), "INBOX"); err != nil {
				t.Fatal(err)
			}
			ctx := safety.AutomaticWrites(context.Background(), pause)
			if err := w.do(ctx, f); !refused(err) {
				t.Fatalf("err = %v, want WriteRefused", err)
			}
			unchanged(t, f)
			if err := w.do(safety.ApprovedWrites(context.Background(), "undo-1"), f); err != nil {
				t.Fatalf("undo outside the automatic scope refused: %v", err)
			}
		})
	}
}

func TestFakeApprovedScopeInShadow(t *testing.T) {
	f := icloud("shadow")
	ctx := safety.ApprovedWrites(context.Background(), "1")
	if _, err := f.SelectRW(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if err := f.StoreFlags(ctx, mail.UIDs(1), true, `\Seen`); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateMailbox(ctx, "New Box"); err != nil {
		t.Fatal(err)
	}
	want := []FakeWrite{
		{Verb: "STORE", Mailbox: "INBOX", UIDs: []uint32{1}, Args: []string{"+FLAGS.SILENT", `\Seen`}},
		{Verb: "CREATE", Mailbox: "New Box"},
	}
	if !reflect.DeepEqual(f.Writes, want) {
		t.Fatalf("writes = %+v, want %+v", f.Writes, want)
	}
	if err := f.CreateMailbox(context.Background(), "x"); !refused(err) {
		t.Fatalf("create outside approval = %v", err)
	}
	if len(f.Writes) != 2 {
		t.Fatalf("refused create recorded: %+v", f.Writes)
	}
}

func TestFakeOperatorSendAppendsOnlyToSent(t *testing.T) {
	f := icloud("shadow")
	raw := []byte("From: me@x.test\r\n\r\nhi")
	if err := f.Append(context.Background(), "Sent Messages", raw, `\Seen`); !refused(err) {
		t.Fatalf("append outside operator send = %v", err)
	}
	ctx := safety.OperatorSend(context.Background(), "Sent Messages")
	if err := f.Append(ctx, "INBOX", raw, `\Seen`); !refused(err) {
		t.Fatalf("append to INBOX = %v", err)
	}
	if err := f.StoreFlags(ctx, mail.UIDs(1), true, `\Seen`); !refused(err) {
		t.Fatalf("store under operator send = %v", err)
	}
	if err := f.Append(ctx, "Sent Messages", raw, `\Seen`); err != nil {
		t.Fatal(err)
	}
	want := []FakeAppend{{Mailbox: "Sent Messages", Raw: raw, Flags: []string{`\Seen`}}}
	if !reflect.DeepEqual(f.Appended, want) {
		t.Fatalf("appended = %+v", f.Appended)
	}
}

func TestFakeLiveMoveIsCopyStoreExpunge(t *testing.T) {
	f := icloud("live")
	ctx := context.Background()
	if _, err := f.SelectRW(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	copied, err := f.Copy(ctx, mail.UIDs(1), "Receipts")
	if err != nil {
		t.Fatal(err)
	}
	if want := (mail.CopyUID{UIDValidity: 5, Mapping: map[uint32]uint32{1: 1}}); !reflect.DeepEqual(*copied, want) {
		t.Fatalf("copyuid = %+v", copied)
	}
	if err := f.StoreFlags(ctx, mail.UIDs(1), true, `\Deleted`); err != nil {
		t.Fatal(err)
	}
	if err := f.UIDExpunge(ctx, mail.UIDs(1)); err != nil {
		t.Fatal(err)
	}
	if len(f.Boxes["INBOX"].Messages) != 0 || len(f.Boxes["Receipts"].Messages) != 1 {
		t.Fatalf("move left INBOX %d, Receipts %d", len(f.Boxes["INBOX"].Messages), len(f.Boxes["Receipts"].Messages))
	}
	var verbs []string
	for _, w := range f.Writes {
		verbs = append(verbs, w.Verb)
	}
	if !reflect.DeepEqual(verbs, []string{"COPY", "STORE", "EXPUNGE"}) {
		t.Fatalf("verbs = %v", verbs)
	}
}

func TestFakeGmailLabelsAndFailingStore(t *testing.T) {
	f := NewFakeClient(env("live"))
	f.Gmail = true
	all := f.AddBox(gmailAllMail, NewBox(1), `\All`)
	h, b := Raw("stuck")
	all.Add(h, b, []string{`\Seen`}, []string{inboxLabel})
	f.FailWrite = func(w FakeWrite) error {
		if w.Verb == "STORE" && w.Args[0] == "-X-GM-LABELS" {
			return errors.New("NO try later")
		}
		return nil
	}
	ctx := context.Background()
	if _, err := f.SelectRW(ctx, gmailAllMail); err != nil {
		t.Fatal(err)
	}
	if err := f.StoreLabels(ctx, mail.UIDs(1), true, "Receipts"); err != nil {
		t.Fatal(err)
	}
	if err := f.StoreLabels(ctx, mail.UIDs(1), false, inboxLabel); err == nil || err.Error() != "NO try later" {
		t.Fatalf("failing store = %v", err)
	}
	if got := all.Messages[1].Labels; !reflect.DeepEqual(got, []string{inboxLabel, "Receipts"}) {
		t.Fatalf("labels = %v", got)
	}
	f.FailWrite = nil
	if err := f.StoreLabels(ctx, mail.UIDs(1), false, inboxLabel); err != nil {
		t.Fatal(err)
	}
	if got := all.Messages[1].Labels; !reflect.DeepEqual(got, []string{"Receipts"}) {
		t.Fatalf("labels = %v", got)
	}
	st, err := f.Status(ctx, "Receipts")
	if err != nil || st.Messages != 1 {
		t.Fatalf("label status = %+v, %v", st, err)
	}
}

func TestFakeReads(t *testing.T) {
	f := icloud("shadow")
	ctx := context.Background()
	boxes, _ := f.ListMailboxes(ctx)
	if len(boxes) != 3 || boxes[2].IMAPName != "Sent Messages" || !reflect.DeepEqual(boxes[2].Flags, []string{`\Sent`}) {
		t.Fatalf("boxes = %+v", boxes)
	}
	if err := f.Select(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	inbox := f.Boxes["INBOX"]
	h, b := Raw("second")
	inbox.Add(h, b, nil, nil)
	st, _ := f.Status(ctx, "INBOX")
	if st.UIDValidity != 1 || st.UIDNext != 3 || st.Messages != 2 || *st.HighestModSeq != 3 {
		t.Fatalf("status = %+v", st)
	}
	uids, _ := f.UIDSearch(ctx, mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: "<second@x.test>"})
	if !reflect.DeepEqual(uids, []uint32{2}) {
		t.Fatalf("header search = %v", uids)
	}
	set := mail.UIDs(1, 9)
	uids, _ = f.UIDSearch(ctx, mail.SearchCriteria{UID: &set})
	if !reflect.DeepEqual(uids, []uint32{1}) {
		t.Fatalf("uid search = %v", uids)
	}
	msgs, _ := f.UIDFetch(ctx, mail.AllUIDs(), mail.FetchItems{ChangedSince: 2})
	if len(msgs) != 1 || msgs[0].UID != 2 || msgs[0].Header != nil || msgs[0].Attachments != nil {
		t.Fatalf("changedsince fetch = %+v", msgs)
	}
	msgs, _ = f.UIDFetch(ctx, mail.UIDs(1), mail.FetchItems{Body: true})
	if len(msgs) != 1 || string(msgs[0].Text) != "hello" || msgs[0].Attachments == nil || msgs[0].Labels != nil {
		t.Fatalf("body fetch = %+v", msgs)
	}
}
