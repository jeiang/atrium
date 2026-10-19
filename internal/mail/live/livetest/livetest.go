// Package livetest is tests/test_live.py's World: a synced FakeClient account past go-live, and the automatic
// writes the applier makes, done with mailops and logged as it logs them.
package livetest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	Seen    = mailops.Seen
	Flagged = mailops.Flagged
)

// Seed is a message present before go-live.
type Seed struct {
	Subject string
	Flags   []string
	Labels  []string
}

type World struct {
	T        *testing.T
	Account  string
	Mode     string
	DB       *db.DB
	Cfg      config.Config
	Fake     *mailtest.FakeClient
	Syncer   *mailsync.Syncer
	Registry *proposals.Registry
	Live     *live.Live
	Clock    float64
	Synced   []string
	Opened   int
}

func newWorld(t *testing.T, account, mode string, boxes func(*mailtest.FakeClient), seed func(*World)) *World {
	t.Helper()
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_MODE":      mode,
		"ATRIUM_DB":        filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	w := &World{T: t, Account: account, Mode: mode, DB: dbtest.Schema(t), Cfg: cfg, Fake: mailtest.NewFakeClient(env), Clock: 1_792_080_000}
	for _, a := range [...]string{config.ICloud, config.Gmail} {
		w.exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", a, a)
	}
	clock := func() float64 { return w.Clock }
	w.Syncer = &mailsync.Syncer{DB: w.DB, Cfg: cfg, Now: clock}
	w.Registry = proposals.NewRegistry()
	live.RegisterUndoers(w.Registry, w.Open)
	w.Registry.Sync = func(ctx context.Context, account string) error {
		w.Synced = append(w.Synced, account)
		_, err := w.Syncer.Account(ctx, account, w.Fake)
		return err
	}
	w.Live = &live.Live{DB: w.DB, Cfg: cfg, Registry: w.Registry, Now: clock}
	boxes(w.Fake)
	seed(w)
	w.Sync()
	if _, err := w.Live.EnsureGolive(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	w.Clock += 60
	return w
}

// ICloud is icloud(): INBOX, Bills (validity 3), Misc (4), Deleted Messages (9) and Sent Messages (5).
func ICloud(t *testing.T, mode string, seed ...Seed) *World {
	return newWorld(t, config.ICloud, mode, func(f *mailtest.FakeClient) {
		f.AddBox(config.Inbox, mailtest.NewBox(1))
		f.AddBox("Bills", mailtest.NewBox(3))
		f.AddBox("Misc", mailtest.NewBox(4))
		f.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
		f.AddBox("Sent Messages", mailtest.NewBox(5), `\Sent`)
	}, func(w *World) {
		for _, s := range seed {
			w.add(w.Fake.Boxes[config.Inbox], s.Subject, "a@x.test", s.Flags, nil)
		}
	})
}

// Gmail is gmail(): [Gmail]/All Mail and [Gmail]/Trash (validity 5).
func Gmail(t *testing.T, mode string, seed ...Seed) *World {
	return newWorld(t, config.Gmail, mode, func(f *mailtest.FakeClient) {
		f.Gmail = true
		f.AddBox(config.GmailAllMail, mailtest.NewBox(1))
		f.AddBox("[Gmail]/Trash", mailtest.NewBox(5), `\Trash`)
	}, func(w *World) {
		for _, s := range seed {
			w.add(w.Fake.Boxes[config.GmailAllMail], s.Subject, "a@x.test", s.Flags, s.Labels)
		}
	})
}

func (w *World) exec(query string, args ...any) {
	w.T.Helper()
	if _, err := w.DB.Write.Exec(query, args...); err != nil {
		w.T.Fatal(err)
	}
}

// Exec runs one statement on the world's database.
func (w *World) Exec(query string, args ...any) { w.T.Helper(); w.exec(query, args...) }

// Open is the account opener every undo uses.
func (w *World) Open(context.Context, string) (mail.Client, error) {
	w.Opened++
	return w.Fake, nil
}

func (w *World) add(box *mailtest.FakeBox, subject, sender string, flags, labels []string) uint32 {
	header, text := mailtest.RawWith(subject, sender, subject, "hello")
	return box.Add(header, text, flags, labels)
}

// InboxBox is the account's Inbox mailbox (Gmail: All Mail).
func (w *World) InboxBox() *mailtest.FakeBox {
	if w.Account == config.Gmail {
		return w.Fake.Boxes[config.GmailAllMail]
	}
	return w.Fake.Boxes[config.Inbox]
}

// Arrive is arrive(subject, flags=(SEEN,)) into the Inbox, Gmail with the \Inbox label, then a sync.
func (w *World) Arrive(subject string, flags ...string) uint32 {
	w.T.Helper()
	var labels []string
	if w.Account == config.Gmail {
		labels = []string{mailops.InboxLabel}
	}
	if flags == nil {
		flags = []string{Seen}
	}
	uid := w.add(w.InboxBox(), subject, "a@shop.test", flags, labels)
	w.Sync()
	return uid
}

func (w *World) Sync() {
	w.T.Helper()
	if _, err := w.Syncer.Account(context.Background(), w.Account, w.Fake); err != nil {
		w.T.Fatal(err)
	}
}

// MID is mid: the newest live message with this subject.
func (w *World) MID(subject string) int64 {
	w.T.Helper()
	var id int64
	if err := w.DB.Read.QueryRow("SELECT id FROM messages WHERE subject = ? AND gone_at IS NULL ORDER BY id DESC", subject).Scan(&id); err != nil {
		w.T.Fatalf("message %q: %v", subject, err)
	}
	return id
}

// Decide is decide: a routing decision for the message.
func (w *World) Decide(subject, action string, dest *string, stage string) int64 {
	w.T.Helper()
	mid := w.MID(subject)
	w.exec("INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
		"VALUES (?, ?, 1, ?, 'k', ?, ?, 0.9, '{}')", mid, w.Account, stage, action, dest)
	return mid
}

// Actions is every auto_actions row, oldest first.
func (w *World) Actions() []live.Action {
	w.T.Helper()
	rows, err := w.Live.DB.Read.Query("SELECT id FROM auto_actions ORDER BY id")
	if err != nil {
		w.T.Fatal(err)
	}
	ids, err := db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		err := r.Scan(&id)
		return id, err
	})
	if err != nil {
		w.T.Fatal(err)
	}
	out := make([]live.Action, len(ids))
	for i, id := range ids {
		a, err := live.Get(context.Background(), w.DB.Read, id)
		if err != nil || a == nil {
			w.T.Fatalf("action %d: %v", id, err)
		}
		out[i] = *a
	}
	return out
}

// Automatic is the scope the applier writes in.
func (w *World) Automatic() context.Context {
	return safety.AutomaticWrites(context.Background(), w.Cfg.PausePath())
}

func (w *World) verified(subject string) mailops.Verified {
	w.T.Helper()
	snaps, err := mailops.Snapshots(context.Background(), w.DB.Read, []int64{w.MID(subject)})
	if err != nil {
		w.T.Fatal(err)
	}
	ok, skipped, err := mailops.Verify(context.Background(), w.DB.Read, w.Fake, w.Account, snaps)
	if err != nil || len(ok) != 1 {
		w.T.Fatalf("verify %q: %v %v %v", subject, ok, skipped, err)
	}
	return ok[0]
}

func before(v mailops.Verified) py.Dict {
	return py.Dict{
		{Key: "imap_name", Value: v.IMAPName}, {Key: "uidvalidity", Value: v.UIDValidity}, {Key: "uid", Value: v.UID},
		{Key: "msgid", Value: v.MsgID}, {Key: "flags", Value: v.Flags}, {Key: "labels", Value: v.Labels},
	}
}

func (w *World) begin(v mailops.Verified, kind string, dest *string) int64 {
	w.T.Helper()
	id, ok, err := w.Live.Begin(context.Background(), w.Account, v.ID, v.Digest, kind, new(v.IMAPName), dest, before(v), nil)
	if err != nil || !ok {
		w.T.Fatalf("begin %s: %v %v", kind, ok, err)
	}
	return id
}

// File is the applier's iCloud move of one verified message to dest, logged with its move_back undo plan.
func (w *World) File(subject, kind, dest string) int64 {
	w.T.Helper()
	v := w.verified(subject)
	id := w.begin(v, kind, &dest)
	ctx := w.Automatic()
	err := mailops.MoveUIDs(ctx, w.Fake, v.IMAPName, dest, []uint32{v.UID}, func(_ []uint32, copied *mail.CopyUID) error {
		var validity, destUID any
		if copied != nil {
			validity = copied.UIDValidity
			if u, ok := copied.Mapping[v.UID]; ok {
				destUID = u
			}
		}
		after := py.Dict{{Key: "imap_name", Value: dest}, {Key: "uidvalidity", Value: validity}, {Key: "uid", Value: destUID}}
		undo := py.Dict{
			{Key: "op", Value: live.OpMoveBack}, {Key: "account", Value: w.Account}, {Key: "src_imap", Value: v.IMAPName},
			{Key: "dest_imap", Value: dest}, {Key: "dest_uidvalidity", Value: validity},
			{Key: "items", Value: []any{py.Dict{{Key: "id", Value: v.ID}, {Key: "msgid", Value: v.MsgID}, {Key: "uid", Value: v.UID}, {Key: "dest_uid", Value: destUID}}}},
		}
		return w.Live.Finish(ctx, id, after, undo, nil)
	})
	if err != nil {
		w.T.Fatal(err)
	}
	return id
}

// Relabel is the applier's Gmail filing: dest added, \Inbox removed, with its gmail_labels undo plan.
func (w *World) Relabel(subject, dest string) int64 {
	w.T.Helper()
	v := w.verified(subject)
	id := w.begin(v, live.File, &dest)
	ctx := w.Automatic()
	if err := mailops.StoreLabels(ctx, w.Fake, v.IMAPName, []uint32{v.UID}, []string{dest}, []string{mailops.InboxLabel}); err != nil {
		w.T.Fatal(err)
	}
	undo := py.Dict{
		{Key: "op", Value: live.OpGmailLabels}, {Key: "imap_name", Value: v.IMAPName}, {Key: "uidvalidity", Value: v.UIDValidity},
		{Key: "uids", Value: []uint32{v.UID}}, {Key: "add", Value: []string{mailops.InboxLabel}}, {Key: "remove", Value: []string{dest}},
	}
	after := py.Dict{{Key: "add", Value: []string{dest}}, {Key: "remove", Value: []string{mailops.InboxLabel}}}
	if err := w.Live.Finish(ctx, id, after, undo, nil); err != nil {
		w.T.Fatal(err)
	}
	return id
}

// Flag is the applier's contacts flag with its unflag undo plan.
func (w *World) Flag(subject string) int64 {
	w.T.Helper()
	v := w.verified(subject)
	id := w.begin(v, live.Flag, nil)
	ctx := w.Automatic()
	if err := mailops.StoreFlag(ctx, w.Fake, v.IMAPName, []uint32{v.UID}, mailops.Flagged, true); err != nil {
		w.T.Fatal(err)
	}
	undo := py.Dict{
		{Key: "op", Value: live.OpUnflag}, {Key: "account", Value: w.Account}, {Key: "imap_name", Value: v.IMAPName},
		{Key: "uidvalidity", Value: v.UIDValidity}, {Key: "uids", Value: []uint32{v.UID}},
	}
	if err := w.Live.Finish(ctx, id, py.Dict{{Key: "flag", Value: mailops.Flagged}}, undo, nil); err != nil {
		w.T.Fatal(err)
	}
	return id
}

// GmailTrash is the applier's Gmail trash copy with its gmail_untrash undo plan.
func (w *World) GmailTrash(subject string) int64 {
	w.T.Helper()
	v := w.verified(subject)
	ctx := w.Automatic()
	trash, err := mailops.TrashMailbox(ctx, w.Fake, w.Account)
	if err != nil {
		w.T.Fatal(err)
	}
	id := w.begin(v, live.Trash, &trash)
	if err := w.Fake.SelectRW(ctx, v.IMAPName); err != nil {
		w.T.Fatal(err)
	}
	copied, err := mailops.CopyUIDs(ctx, w.Fake, trash, []uint32{v.UID})
	if err != nil || copied == nil {
		w.T.Fatalf("copy: %v %v", copied, err)
	}
	destUID := copied.Mapping[v.UID]
	undo := py.Dict{
		{Key: "op", Value: live.OpGmailUntrash}, {Key: "trash_imap", Value: trash},
		{Key: "dest_uidvalidity", Value: copied.UIDValidity}, {Key: "uids", Value: []uint32{destUID}},
	}
	after := py.Dict{{Key: "imap_name", Value: trash}, {Key: "uidvalidity", Value: copied.UIDValidity}, {Key: "uid", Value: destUID}}
	if err := w.Live.Finish(ctx, id, after, undo, nil); err != nil {
		w.T.Fatal(err)
	}
	return id
}
