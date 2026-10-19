package mailops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/live/livetest"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

// recorder logs every client call in order.
type recorder struct {
	mail.Client
	calls []string
}

func (r *recorder) log(s string) { r.calls = append(r.calls, s) }

func (r *recorder) Status(ctx context.Context, name string) (mail.Status, error) {
	r.log("STATUS " + name)
	return r.Client.Status(ctx, name)
}

func (r *recorder) Select(ctx context.Context, name string) error {
	r.log("SELECT " + name)
	return r.Client.Select(ctx, name)
}

func (r *recorder) SelectRW(ctx context.Context, name string) error {
	r.log("SELECT-RW " + name)
	return r.Client.SelectRW(ctx, name)
}

func (r *recorder) UIDSearch(ctx context.Context, c mail.SearchCriteria) ([]uint32, error) {
	r.log("SEARCH")
	return r.Client.UIDSearch(ctx, c)
}

func (r *recorder) UIDFetch(ctx context.Context, uids mail.UIDSet, items mail.FetchItems) ([]mail.Message, error) {
	r.log("FETCH " + uids.String())
	return r.Client.UIDFetch(ctx, uids, items)
}

func (r *recorder) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	r.log("COPY " + uids.String())
	return r.Client.Copy(ctx, uids, dest)
}

func (r *recorder) StoreFlags(ctx context.Context, uids mail.UIDSet, add bool, flags ...string) error {
	r.log("STORE " + uids.String())
	return r.Client.StoreFlags(ctx, uids, add, flags...)
}

func (r *recorder) UIDExpunge(ctx context.Context, uids mail.UIDSet) error {
	r.log("EXPUNGE " + uids.String())
	return r.Client.UIDExpunge(ctx, uids)
}

func snapshots(t *testing.T, w *livetest.World, subjects ...string) []mailops.Snapshot {
	t.Helper()
	var ids []int64
	for _, s := range subjects {
		ids = append(ids, w.MID(s))
	}
	snaps, err := mailops.Snapshots(t.Context(), w.DB.Read, ids)
	if err != nil {
		t.Fatal(err)
	}
	return snaps
}

func TestSnapshotsDict(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("b")
	w.Arrive("a")
	snaps := snapshots(t, w, "a", "b")
	if snaps[0].ID >= snaps[1].ID || snaps[0].MsgID != "b@x.test" || snaps[0].IMAPName != "INBOX" || snaps[0].Mailbox != "INBOX" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	want := `{"id": 1, "uid": 1, "uidvalidity": 1, "msgid": "b@x.test", "digest": "` + snaps[0].Digest +
		`", "mailbox": "INBOX", "imap_name": "INBOX"}`
	if got := py.JSONDumps(snaps[0].Dict()); got != want {
		t.Fatalf("Dict = %s", got)
	}
}

func TestVerifySkipReasonsInPythonOrder(t *testing.T) {
	w := livetest.ICloud(t, "live")
	for _, s := range []string{"ok", "gone", "moved", "changed", "missing", "msgid"} {
		w.Arrive(s)
	}
	h, b := mailtest.RawWith("bill", "a@x.test", "bill", "x")
	w.Fake.Boxes["Bills"].Add(h, b, []string{livetest.Seen}, nil)
	w.Sync()
	snaps := snapshots(t, w, "ok", "gone", "moved", "changed", "missing", "msgid", "bill")
	w.Exec("UPDATE messages SET gone_at = 1 WHERE subject = 'gone'")
	w.Exec("UPDATE messages SET uid = 77 WHERE subject = 'moved'")
	w.Exec("UPDATE messages SET digest = 'new' WHERE subject = 'changed'")
	inbox := w.Fake.Boxes["INBOX"]
	inbox.Remove(5)
	other, _ := mailtest.RawWith("msgid", "a@x.test", "other", "x")
	inbox.Messages[6].Header = other
	w.Fake.Boxes["Bills"].Validity = 30
	rec := &recorder{Client: w.Fake}
	ok, skipped, err := mailops.Verify(t.Context(), w.DB.Read, rec, config.ICloud, snaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 1 || ok[0].ID != w.MID("ok") || !reflect.DeepEqual(ok[0].Flags, []string{livetest.Seen}) || ok[0].Labels == nil {
		t.Fatalf("ok = %+v", ok)
	}
	var got []string
	for _, s := range skipped {
		got = append(got, s.Reason)
	}
	want := []string{"gone", "moved", "changed", "missing", "msgid", "uidvalidity"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skip reasons = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(rec.calls, []string{"STATUS INBOX", "SELECT INBOX", "SEARCH", "FETCH 1,6", "STATUS Bills"}) {
		t.Fatalf("calls = %v", rec.calls)
	}
	last := w.Fake.Fetches[len(w.Fake.Fetches)-1].Items
	if last != (mail.FetchItems{HeaderOnly: true}) {
		t.Fatalf("verify fetched %+v", last)
	}
}

func TestVerifyGmailFetchesLabels(t *testing.T) {
	w := livetest.Gmail(t, "live")
	w.Arrive("g")
	ok, skipped, err := mailops.Verify(t.Context(), w.DB.Read, w.Fake, config.Gmail, snapshots(t, w, "g"))
	if err != nil || len(skipped) != 0 || !reflect.DeepEqual(ok[0].Labels, []string{mailops.InboxLabel}) {
		t.Fatalf("Verify = %+v %v %v", ok, skipped, err)
	}
	if items := w.Fake.Fetches[len(w.Fake.Fetches)-1].Items; items != (mail.FetchItems{HeaderOnly: true, Labels: true}) {
		t.Fatalf("items = %+v", items)
	}
}

func TestServerMsgID(t *testing.T) {
	for header, want := range map[string]string{
		"Message-ID: <a@b.test>\r\n\r\n":             "a@b.test",
		"Message-ID:\r\n <folded@b.test>\r\n\r\n":    "folded@b.test",
		"Message-ID: <caf\xc3\xa9@b.test>\r\n\r\n":   "caf\ufffd\ufffd@b.test",
		"Message-ID: <a\xa0b@x>\r\n\r\n":             "a\ufffdb@x",
		"From: a\r\n\r\n":                            "",
		"Message-ID: <=?utf-8?q?a?=@b.test>\r\n\r\n": "=?utf-8?q?a?=@b.test",
	} {
		if got := mailops.ServerMsgID([]byte(header)); got != want {
			t.Errorf("ServerMsgID(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestNoWriteBeforeVerify composes verify and a move the way every group write does: the server is read for
// exactly the snapshots before the first write, and only the verified UIDs are written.
func TestNoWriteBeforeVerify(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("a")
	w.Arrive("b")
	snaps := snapshots(t, w, "a", "b")
	w.Fake.Boxes["INBOX"].Remove(2)
	rec := &recorder{Client: w.Fake}
	ctx := w.Automatic()
	ok, _, err := mailops.Verify(ctx, w.DB.Read, rec, config.ICloud, snaps)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range mailops.GroupByMailbox(ok) {
		var uids []uint32
		for _, it := range g.Items {
			uids = append(uids, it.UID)
		}
		if err := mailops.MoveUIDs(ctx, rec, g.IMAPName, "Bills", uids, func([]uint32, *mail.CopyUID) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"STATUS INBOX", "SELECT INBOX", "SEARCH", "FETCH 1", "SELECT-RW INBOX", "COPY 1", "STORE 1", "EXPUNGE 1"}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestMoveUIDsBatchesAndReportsPartialMove(t *testing.T) {
	w := livetest.ICloud(t, "live")
	inbox := w.Fake.Boxes["INBOX"]
	var all []uint32
	for range mailops.Batch + 1 {
		h, b := mailtest.Raw("m")
		all = append(all, inbox.Add(h, b, nil, nil))
	}
	var batches [][]uint32
	err := mailops.MoveUIDs(t.Context(), w.Fake, "INBOX", "Bills", all, func(batch []uint32, copied *mail.CopyUID) error {
		if copied == nil || copied.UIDValidity != 3 || len(copied.Mapping) != len(batch) {
			t.Fatalf("copied = %+v", copied)
		}
		batches = append(batches, batch)
		return nil
	})
	if err != nil || len(batches) != 2 || len(batches[0]) != mailops.Batch || len(batches[1]) != 1 || len(inbox.Messages) != 0 {
		t.Fatalf("MoveUIDs = %v, batches %d", err, len(batches))
	}
	h, b := mailtest.Raw("half")
	uid := inbox.Add(h, b, nil, nil)
	w.Fake.FailWrite = func(fw mailtest.FakeWrite) error {
		if fw.Verb == "EXPUNGE" {
			return errors.New("connection reset")
		}
		return nil
	}
	err = mailops.MoveUIDs(t.Context(), w.Fake, "INBOX", "Bills", []uint32{uid}, func([]uint32, *mail.CopyUID) error {
		t.Fatal("a partial batch was reported moved")
		return nil
	})
	var partial *mailops.PartialMove
	if !errors.As(err, &partial) || err.Error() != "copied but not expunged: connection reset" ||
		partial.Copied == nil || partial.Copied.Mapping[uid] != mailops.Batch+2 || py.ExceptionText(err) != "PartialMove: copied but not expunged: connection reset" {
		t.Fatalf("partial = %#v", err)
	}
}

func TestMoveUIDsRefusesWithoutUIDPLUS(t *testing.T) {
	w := livetest.ICloud(t, "live")
	w.Arrive("m")
	delete(w.Fake.Caps, "UIDPLUS")
	err := mailops.MoveUIDs(t.Context(), w.Fake, "INBOX", "Bills", []uint32{1}, nil)
	if err == nil || err.Error() != "server lacks UIDPLUS: refusing to move" || w.Fake.RW != "" || len(w.Fake.Writes) != 0 {
		t.Fatalf("MoveUIDs = %v, rw %q, writes %v", err, w.Fake.RW, w.Fake.Writes)
	}
}

func TestFolderHelpers(t *testing.T) {
	w := livetest.ICloud(t, "live")
	ctx := t.Context()
	trash, err := mailops.TrashMailbox(ctx, w.Fake, config.ICloud)
	if err != nil || trash != "Deleted Messages" {
		t.Fatalf("TrashMailbox = %q, %v", trash, err)
	}
	w.Fake.BoxFlags["Deleted Messages"] = nil
	w.Fake.AddBox("Bin", mailtest.NewBox(1))
	if trash, _ := mailops.TrashMailbox(ctx, w.Fake, config.ICloud); trash != "Deleted Messages" {
		t.Fatalf("TrashMailbox by name = %q", trash)
	}
	if _, err := mailops.TrashMailbox(ctx, w.Fake, config.Gmail); err == nil || py.ExceptionText(err) != "MailOpError: no Trash mailbox found" {
		t.Fatalf("Gmail without \\Trash = %v", err)
	}
	created, err := mailops.EnsureFolder(ctx, w.Fake, "Travel")
	if err != nil || !created || !reflect.DeepEqual(verbs(w.Fake), []string{"CREATE", "SUBSCRIBE"}) {
		t.Fatalf("EnsureFolder = %v, %v, %v", created, err, w.Fake.Writes)
	}
	if created, _ := mailops.EnsureFolder(ctx, w.Fake, "Travel"); created {
		t.Fatal("created twice")
	}
	w.Arrive("keep")
	if deleted, _ := mailops.DeleteFolderIfEmpty(ctx, w.Fake, "INBOX"); deleted {
		t.Fatal("deleted a folder with mail")
	}
	if deleted, err := mailops.DeleteFolderIfEmpty(ctx, w.Fake, "Travel"); err != nil || !deleted || w.Fake.Deleted[0] != "Travel" {
		t.Fatalf("DeleteFolderIfEmpty = %v, %v", deleted, err)
	}
}

func verbs(f *mailtest.FakeClient) []string {
	var out []string
	for _, w := range f.Writes {
		out = append(out, w.Verb)
	}
	return out
}

func TestLabelAndFlagStores(t *testing.T) {
	w := livetest.Gmail(t, "live")
	w.Arrive("g")
	ctx := t.Context()
	if err := mailops.StoreLabels(ctx, w.Fake, config.GmailAllMail, []uint32{1}, []string{"Ré"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := mailops.ArchiveUIDs(ctx, w.Fake, config.GmailAllMail, []uint32{1}); err != nil {
		t.Fatal(err)
	}
	if err := mailops.StoreFlag(ctx, w.Fake, config.GmailAllMail, []uint32{1}, mailops.Flagged, true); err != nil {
		t.Fatal(err)
	}
	var args [][]string
	for _, fw := range w.Fake.Writes {
		args = append(args, fw.Args)
	}
	want := [][]string{{"+X-GM-LABELS", "Ré"}, {"-X-GM-LABELS", `\Inbox`}, {"+FLAGS.SILENT", `\Flagged`}}
	if !reflect.DeepEqual(args, want) || w.Fake.RW != config.GmailAllMail {
		t.Fatalf("stores = %v", args)
	}
}

func TestRelabelPlanAndGroups(t *testing.T) {
	item := func(id int64, box string, labels ...string) mailops.Verified {
		return mailops.Verified{Snapshot: mailops.Snapshot{ID: id, IMAPName: box, UIDValidity: 1}, Labels: labels}
	}
	items := []mailops.Verified{item(1, "A", `\Inbox`), item(2, "B", `\Inbox`, "R"), item(3, "A", `\Inbox`)}
	plan := mailops.RelabelPlan(items, []string{"R"}, []string{`\Inbox`})
	if len(plan) != 2 || !reflect.DeepEqual(plan[0].Add, []string{"R"}) || len(plan[0].Items) != 2 ||
		len(plan[1].Add) != 0 || !reflect.DeepEqual(plan[1].Remove, []string{`\Inbox`}) {
		t.Fatalf("RelabelPlan = %+v", plan)
	}
	groups := mailops.GroupByMailbox(items)
	if len(groups) != 2 || groups[0].IMAPName != "A" || len(groups[0].Items) != 2 || groups[1].IMAPName != "B" {
		t.Fatalf("GroupByMailbox = %+v", groups)
	}
	if got := mailops.Chunks([]int{1, 2, 3}, 2); !reflect.DeepEqual(got, [][]int{{1, 2}, {3}}) || mailops.Chunks([]int{}, 2) != nil {
		t.Fatalf("Chunks = %v", got)
	}
}

func refused(err error) bool {
	var r safety.WriteRefused
	return errors.As(err, &r)
}

// TestEveryWriteGoesThroughTheGate runs every write helper of this package and every undoer of live in shadow
// mode with no approval: each is refused before the connection sees a write, and each runs once approved.
func TestEveryWriteGoesThroughTheGate(t *testing.T) {
	type write func(ctx context.Context, w *livetest.World, r *proposals.Registry) error
	undo := func(op string, plan py.Dict) write {
		return func(ctx context.Context, _ *livetest.World, r *proposals.Registry) error {
			_, err := r.Undoers[op](ctx, []byte(py.JSONDumps(plan)))
			return err
		}
	}
	writes := map[string]write{
		"MoveUIDs": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			return mailops.MoveUIDs(ctx, w.Fake, "INBOX", "Bills", []uint32{1}, func([]uint32, *mail.CopyUID) error { return nil })
		},
		"CopyUIDs": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			_, err := mailops.CopyUIDs(ctx, w.Fake, "Misc", []uint32{1})
			return err
		},
		"StoreLabels": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			return mailops.StoreLabels(ctx, w.Fake, "INBOX", []uint32{1}, []string{"x"}, nil)
		},
		"StoreFlag": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			return mailops.StoreFlag(ctx, w.Fake, "INBOX", []uint32{1}, mailops.Flagged, true)
		},
		"ArchiveUIDs": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			return mailops.ArchiveUIDs(ctx, w.Fake, "INBOX", []uint32{1})
		},
		"EnsureFolder": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			_, err := mailops.EnsureFolder(ctx, w.Fake, "New")
			return err
		},
		"DeleteFolderIfEmpty": func(ctx context.Context, w *livetest.World, _ *proposals.Registry) error {
			_, err := mailops.DeleteFolderIfEmpty(ctx, w.Fake, "Misc")
			return err
		},
		"undo " + live.OpMoveBack: undo(live.OpMoveBack, py.Dict{{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: "icloud"},
			{Key: "src_imap", Value: "Bills"}, {Key: "dest_imap", Value: "INBOX"}, {Key: "dest_uidvalidity", Value: 1},
			{Key: "items", Value: []any{py.Dict{{Key: "id", Value: 1}, {Key: "msgid", Value: ""}, {Key: "uid", Value: 1}, {Key: "dest_uid", Value: 1}}}}}),
		"undo " + live.OpGmailLabels: undo(live.OpGmailLabels, py.Dict{{Key: "imap_name", Value: "INBOX"}, {Key: "uidvalidity", Value: 1},
			{Key: "uids", Value: []any{1}}, {Key: "add", Value: []string{"x"}}, {Key: "remove", Value: []string{}}}),
		"undo " + live.OpGmailUntrash: undo(live.OpGmailUntrash, py.Dict{{Key: "trash_imap", Value: "INBOX"},
			{Key: "dest_uidvalidity", Value: nil}, {Key: "uids", Value: []any{1}}}),
		"undo " + live.OpUnflag: undo(live.OpUnflag, py.Dict{{Key: "account", Value: "icloud"}, {Key: "imap_name", Value: "INBOX"},
			{Key: "uidvalidity", Value: 1}, {Key: "uids", Value: []any{1}}}),
	}
	registered := proposals.NewRegistry()
	live.RegisterUndoers(registered, nil)
	for op := range registered.Undoers {
		if writes["undo "+op] == nil {
			t.Fatalf("undoer %s has no gate case", op)
		}
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			w := livetest.ICloud(t, "shadow")
			w.Arrive("m")
			if err := write(t.Context(), w, w.Registry); !refused(err) {
				t.Fatalf("shadow, unapproved = %v", err)
			}
			if err := write(w.Automatic(), w, w.Registry); !refused(err) {
				t.Fatalf("shadow, automatic = %v", err)
			}
			if len(w.Fake.Writes) != 0 || w.Fake.RW != "" {
				t.Fatalf("a refused write reached the connection: %v (rw %q)", w.Fake.Writes, w.Fake.RW)
			}
			if err := write(safety.ApprovedWrites(t.Context(), "undo-1"), w, w.Registry); err != nil || len(w.Fake.Writes) == 0 {
				t.Fatalf("approved = %v, writes %v", err, w.Fake.Writes)
			}
		})
	}
}
