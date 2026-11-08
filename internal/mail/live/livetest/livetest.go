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
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Seen    = mailops.Seen
	Flagged = mailops.Flagged
)

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
	Settings *settings.Registry
	State    *config.State
	Fake     *mailtest.FakeClient
	Syncer   *mailsync.Syncer
	Registry *proposals.Registry
	Live     *live.Live
	Clock    float64
	Synced   []string
	Opened   int
}

func (w *World) Context() context.Context {
	return config.With(settings.With(context.Background(), w.Settings), w.State)
}

func (w *World) ctx() context.Context { return w.Context() }

func newWorld(t *testing.T, account, mode string, boxes func(*mailtest.FakeClient), seed func(*World)) *World {
	t.Helper()
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB":        filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	reg := settings.New()
	w := &World{T: t, Account: account, Mode: mode, DB: dbtest.Schema(t), Cfg: cfg, Settings: reg, State: config.NewState(reg), Fake: mailtest.NewFakeClient(), Clock: 1_792_080_000}
	mailtest.PinMode(t, w.Settings, mode)
	mailtest.UseAccounts(t, w.State, map[string]string{config.ICloud: "me", config.Gmail: "me@gmail.test"})
	for _, a := range [...]string{config.ICloud, config.Gmail} {
		w.exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", a, a)
	}
	clock := func() float64 { return w.Clock }
	w.Syncer = &mailsync.Syncer{DB: w.DB, Cfg: cfg, Now: clock}
	w.Registry = proposals.NewRegistry()
	live.RegisterUndoers(w.Registry, w.Open, w.DB.Read)
	w.Registry.Sync = func(ctx context.Context, account string) error {
		w.Synced = append(w.Synced, account)
		_, err := w.Syncer.Account(ctx, account, w.Client())
		return err
	}
	w.Live = &live.Live{DB: w.DB, Cfg: cfg, Registry: w.Registry, Now: clock}
	boxes(w.Fake)
	seed(w)
	w.Sync()
	if _, err := w.Live.EnsureGolive(w.ctx(), nil); err != nil {
		t.Fatal(err)
	}
	w.Clock += 60
	return w
}

func ICloud(t *testing.T, mode string, seed ...Seed) *World {
	return newWorld(t, config.ICloud, mode, func(f *mailtest.FakeClient) {
		f.AddBox(config.Inbox, mailtest.NewBox(1))
		f.AddBox("Bills", mailtest.NewBox(3))
		f.AddBox("Misc", mailtest.NewBox(4))
		f.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
		f.AddBox("Sent Messages", mailtest.NewBox(5), `\Sent`)
	}, func(w *World) {
		for _, s := range seed {
			w.add(w.Fake.Boxes[config.Inbox].Box, s.Subject, "a@x.test", s.Flags, nil)
		}
	})
}

func (w *World) exec(query string, args ...any) {
	w.T.Helper()
	if _, err := w.DB.Write.Exec(query, args...); err != nil {
		w.T.Fatal(err)
	}
}

func (w *World) Client() mail.Client { return mail.Gated(w.Fake, w.DB.Read) }

func (w *World) Open(_ context.Context, opts mail.OpenOptions) (mail.Client, error) {
	w.Opened++
	return mail.Gated(w.Fake, opts.Q), nil
}

func (w *World) add(box *mailtest.FakeBox, subject, sender string, flags, labels []string) uint32 {
	header, text := mailtest.RawWith(subject, sender, subject, "hello")
	return box.Add(header, text, flags, labels)
}

func (w *World) InboxBox() *mailtest.FakeBox {
	if w.State.Preset(w.Account) == config.Gmail {
		return w.Fake.Boxes[config.GmailAllMail].Box
	}
	return w.Fake.Boxes[config.Inbox].Box
}

func (w *World) Arrive(subject string, flags ...string) uint32 {
	w.T.Helper()
	var labels []string
	if w.State.Preset(w.Account) == config.Gmail {
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
	if _, err := w.Syncer.Account(w.ctx(), w.Account, w.Client()); err != nil {
		w.T.Fatal(err)
	}
}

func (w *World) MID(subject string) int64 {
	w.T.Helper()
	var id int64
	if err := w.DB.Read.QueryRow("SELECT id FROM messages WHERE subject = ? AND gone_at IS NULL ORDER BY id DESC", subject).Scan(&id); err != nil {
		w.T.Fatalf("message %q: %v", subject, err)
	}
	return id
}

func (w *World) Decide(subject, action string, dest *string, stage string) int64 {
	w.T.Helper()
	mid := w.MID(subject)
	w.exec("INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "+
		"VALUES (?, ?, 1, ?, 'k', ?, ?, 0.9, '{}')", mid, w.Account, stage, action, dest)
	return mid
}

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
		a, err := live.Get(w.ctx(), w.DB.Read, id)
		if err != nil || a == nil {
			w.T.Fatalf("action %d: %v", id, err)
		}
		out[i] = *a
	}
	return out
}
