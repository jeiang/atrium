package sync_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
)

type harness struct {
	t     *testing.T
	d     *db.DB
	cfg   config.Config
	fake  *mailtest.FakeClient
	s     *mailsync.Syncer
	clock float64
}

var flagsByMailbox = map[string][]string{"Deleted Messages": {`\Trash`}, "Sent Messages": {`\Sent`}}

func newHarness(t *testing.T, mode string) *harness {
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
	h := &harness{t: t, d: dbtest.Schema(t), cfg: cfg, fake: mailtest.NewFakeClient(env), clock: 1_792_080_000}
	h.s = &mailsync.Syncer{DB: h.d, Cfg: cfg, Now: func() float64 { return h.clock }}
	if _, err := h.d.Write.Exec("DELETE FROM mailboxes"); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) icloudBoxes() map[string]*mailtest.FakeBox {
	boxes := map[string]*mailtest.FakeBox{}
	for _, name := range []string{"INBOX", "Bills", "Misc", "Deleted Messages", "Sent Messages"} {
		boxes[name] = h.fake.AddBox(name, mailtest.NewBox(1), flagsByMailbox[name]...)
	}
	return boxes
}

func (h *harness) sync(account string) mailsync.Stats {
	h.t.Helper()
	stats, err := h.s.Account(context.Background(), account, h.fake)
	if err != nil {
		h.t.Fatal(err)
	}
	return stats
}

type row map[string]any

func (h *harness) query(query string, args ...any) []row {
	h.t.Helper()
	rows, err := h.d.Read.Query(query, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return scanRows(h.t, rows)
}

func scanRows(t *testing.T, rows *sql.Rows) []row {
	t.Helper()
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		r := row{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) messages(where string, args ...any) []row {
	h.t.Helper()
	return h.query("SELECT * FROM messages WHERE "+where+" ORDER BY id", args...)
}

func (h *harness) count(query string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.d.Read.QueryRow(query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func add(box *mailtest.FakeBox, subject string, flags, labels []string) uint32 {
	header, text := mailtest.Raw(subject)
	return box.Add(header, text, flags, labels)
}

type selectRecorder struct {
	mail.Client
	selected []string
}

func (s *selectRecorder) Select(ctx context.Context, imapName string) error {
	s.selected = append(s.selected, imapName)
	return s.Client.Select(ctx, imapName)
}
