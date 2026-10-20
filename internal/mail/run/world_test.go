package run

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
)

var credentials = map[string]string{
	"ICLOUD_MAIL_USERNAME": "u",
	"ICLOUD_APP_PASSWORD":  "p",
	"GMAIL_ADDRESS":        "g",
	"GMAIL_APP_PASSWORD":   "p",
}

// world is one database with a FakeClient per account and a Pass over them. Fail makes the opener fail on the
// account's nth open; Wrap replaces the client it returns.
type world struct {
	t     *testing.T
	env   config.Env
	cfg   config.Config
	db    *db.DB
	fakes map[string]*mailtest.FakeClient
	pass  *Pass

	mu     sync.Mutex
	opened []string
	ctxs   []context.Context
	Fail   func(account string, n int) error
	Wrap   func(account string, c mail.Client) mail.Client
}

func newWorld(t *testing.T, mode string, creds map[string]string) *world {
	t.Helper()
	dir := t.TempDir()
	vars := map[string]string{
		"ATRIUM_MODE":      mode,
		"ATRIUM_DB":        filepath.Join(dir, "atrium.db"),
		"ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
	}
	for k, v := range creds {
		vars[k] = v
	}
	env := config.Map(vars)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d, cfg.DBPath); err != nil {
		t.Fatal(err)
	}
	icloud := mailtest.NewFakeClient(env)
	icloud.AddBox(config.Inbox, mailtest.NewBox(1))
	icloud.AddBox("Bills", mailtest.NewBox(3))
	icloud.AddBox("Misc", mailtest.NewBox(4))
	icloud.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
	gmail := mailtest.NewFakeClient(env)
	gmail.Gmail = true
	gmail.AddBox(config.GmailAllMail, mailtest.NewBox(1))
	gmail.AddBox("[Gmail]/Trash", mailtest.NewBox(5), `\Trash`)
	w := &world{t: t, env: env, cfg: cfg, db: d, fakes: map[string]*mailtest.FakeClient{config.ICloud: icloud, config.Gmail: gmail}}
	w.pass = New(d, cfg, w.open, nil)
	return w
}

func (w *world) open(ctx context.Context, account string) (mail.Client, error) {
	w.mu.Lock()
	w.opened = append(w.opened, account)
	w.ctxs = append(w.ctxs, ctx)
	n := 0
	for _, a := range w.opened {
		if a == account {
			n++
		}
	}
	w.mu.Unlock()
	if w.Fail != nil {
		if err := w.Fail(account, n); err != nil {
			return nil, err
		}
	}
	if w.Wrap != nil {
		return w.Wrap(account, w.fakes[account]), nil
	}
	return w.fakes[account], nil
}

// watcher is a Watcher whose cycles build the world's pass, configuration and hooks over their own connection.
func (w *world) watcher(sleep func(context.Context, time.Duration)) *Watcher {
	return &Watcher{Cfg: w.pass.Cfg, Open: w.open, Sleep: sleep, Pass: func(d *db.DB) *Pass {
		p := New(d, w.pass.Cfg, w.open, nil)
		p.Events = w.pass.Events
		return p
	}}
}

func (w *world) opens() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.opened...)
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Write.Exec(query, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.db.Read.QueryRow(query, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// arrive puts a read message from sender into the account's Inbox.
func (w *world) arrive(account, subject, sender string) {
	header, text := mailtest.RawWith(subject, sender, subject, "hello")
	if account == config.Gmail {
		w.fakes[account].Boxes[config.GmailAllMail].Add(header, text, []string{`\Seen`}, []string{`\Inbox`})
		return
	}
	w.fakes[account].Boxes[config.Inbox].Add(header, text, []string{`\Seen`}, nil)
}

// rule files mail from domain into dest on every account.
func (w *world) rule(domain, dest string) {
	w.exec("INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (0, ?, NULL, 'file', ?, ?)",
		domain, dest, `[{"field": "from_domain", "op": "eq", "value": "`+domain+`"}]`)
}

// logs is the default slog output for the test, safe for the watch goroutines.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logs) has(t *testing.T, parts ...string) {
	t.Helper()
	for _, line := range strings.Split(l.String(), "\n") {
		found := true
		for _, p := range parts {
			found = found && strings.Contains(line, p)
		}
		if found {
			return
		}
	}
	t.Errorf("no log line with %q in:\n%s", parts, l.String())
}

func captureLogs(t *testing.T) *logs {
	t.Helper()
	l := &logs{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

// passContext is the context the next pass hands its first connection: it ends when the pass deadline does.
func (w *world) passContext() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctxs[0]
}

func (w *world) forgetContexts() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ctxs = nil
}
