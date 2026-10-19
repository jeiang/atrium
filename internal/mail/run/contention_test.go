package run

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/pages"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

// webSite is the mail pages served over the world's database on their own connection, as atrium-web is: a
// separate reader and writer pool and a separate flock handle for its corrections.
type webSite struct {
	t      *testing.T
	srv    *web.Server
	cookie string
	token  string
}

func newWebSite(t *testing.T, w *world) *webSite {
	t.Helper()
	d, err := db.Connect(w.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	render, err := web.NewRenderer(w.cfg, d, tpl.Config{Modules: []fs.FS{pages.Templates()}, Globals: pages.Globals()})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{
		Cfg: w.cfg, Env: w.env, DB: d, Auth: web.AuthLocal, AllowedHosts: []string{"testserver"}, Render: render,
		Modules: []web.Module{pages.Module(pages.Options{Open: w.open})},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &webSite{t: t, srv: srv}
	rec := s.do("GET", "/rules", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.CSRFCookie {
			s.cookie, s.token = c.Name+"="+c.Value, c.Value
		}
	}
	if s.token == "" {
		t.Fatal("no CSRF cookie issued")
	}
	return s
}

func (s *webSite) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = "testserver"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
	}
	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
		req.Header.Set(web.CSRFHeader, s.token)
	}
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	return rec
}

// slowMailbox is a client whose fetches and copies take a while, so a pass holds the lock long enough for the
// other holders to queue on it.
type slowMailbox struct{ mail.Client }

func (s slowMailbox) UIDFetch(ctx context.Context, uids mail.UIDSet, items mail.FetchItems) ([]mail.Message, error) {
	time.Sleep(20 * time.Millisecond)
	return s.Client.UIDFetch(ctx, uids, items)
}

func (s slowMailbox) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	time.Sleep(20 * time.Millisecond)
	return s.Client.Copy(ctx, uids, dest)
}

// TestWatchWebCorrectionAndSyncContendForTheLock runs the three holders of atrium.db.lock at once in live mode:
// the watch loop passing after every Inbox change, a web correction moving a message, and `atrium sync`. Each
// takes the flock on its own handle, as separate processes do. Every one must finish, every automatic write
// must be recorded exactly once and applied once, and no ledger row may be left in flight.
func TestWatchWebCorrectionAndSyncContendForTheLock(t *testing.T) {
	const filed = 12
	w := newWorld(t, config.Live, credentials)
	w.fakes[config.ICloud].Idle = true
	w.Wrap = func(account string, _ mail.Client) mail.Client {
		shared := w.fakes[account]
		session := mailtest.NewFakeClient(w.env)
		session.Boxes, session.Order, session.Names, session.BoxFlags = shared.Boxes, shared.Order, shared.Names, shared.BoxFlags
		session.Gmail, session.Idle = shared.Gmail, shared.Idle
		return slowMailbox{session}
	}
	w.rule("x.test", "Bills")
	if err := w.pass.Locked(context.Background(), []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	header, text := mailtest.RawWith("Needs a correction", "o@y.test", "needs-a-correction", "hello")
	corrected := w.fakes[config.ICloud].Boxes[config.Inbox].Add(header, text, []string{`\Seen`}, nil)
	for i := range filed {
		w.arrive(config.ICloud, fmt.Sprintf("invoice-%d", i), "a@x.test")
	}
	site := newWebSite(t, w)

	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	var loops sync.WaitGroup
	settle := func(ctx context.Context, _ time.Duration) { sleepCtx(ctx, 10*time.Millisecond) }
	loops.Go(func() { w.watcher(settle).Account(watchCtx, config.ICloud) })

	waitFor(t, 10*time.Second, func() bool { return w.count("SELECT COUNT(*) FROM messages WHERE uid = ?", corrected) == 1 })
	var id int64
	if err := w.db.Read.QueryRow("SELECT id FROM messages WHERE uid = ?", corrected).Scan(&id); err != nil {
		t.Fatal(err)
	}

	var correction sync.WaitGroup
	var status int
	var waited time.Duration
	correction.Go(func() {
		start := time.Now()
		rec := site.do("POST", fmt.Sprintf("/messages/%d/correct", id), url.Values{"dest": {"Bills"}, "context": {"message"}})
		waited = time.Since(start)
		status = rec.Code
	})
	code, _, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud")
	if code != cli.ExitOK {
		t.Fatalf("sync = %d, %q", code, stderr)
	}
	correction.Wait()
	if status != http.StatusOK {
		t.Fatalf("correction = %d", status)
	}
	waitFor(t, 10*time.Second, func() bool {
		return w.count("SELECT COUNT(*) FROM auto_actions WHERE ok = 1") == filed+1
	}, func() string { return fmt.Sprint(liveActions(w)) })
	stopWatch()
	loops.Wait()

	inbox, bills := w.fakes[config.ICloud].Boxes[config.Inbox], w.fakes[config.ICloud].Boxes["Bills"]
	if len(inbox.Messages) != 0 || len(bills.Messages) != filed+1 {
		t.Errorf("Inbox holds %d messages and Bills %d", len(inbox.Messages), len(bills.Messages))
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions"); n != filed+1 {
		t.Errorf("%d auto_actions rows, want %d", n, filed+1)
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE kind = 'correct' AND ok = 1 AND message_id = ?", id); n != 1 {
		t.Errorf("%d applied correct rows for the corrected message", n)
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE ok IS NULL OR error IS NOT NULL"); n != 0 {
		t.Errorf("%d rows in flight or failed", n)
	}
	if n := w.count("SELECT COUNT(*) FROM messages WHERE gone_at IS NULL AND folder = 'Bills'"); n != filed+1 {
		t.Errorf("%d live rows in Bills", n)
	}
	t.Logf("web correction answered %d after %v while watch and sync held the lock", status, waited)
}

func waitFor(t *testing.T, limit time.Duration, ok func() bool, state ...func() string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			detail := ""
			for _, s := range state {
				detail += ": " + s()
			}
			t.Fatal("condition not met in time" + detail)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
