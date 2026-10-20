package run

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
)

// imaplibError stands for imapclient.Error: an imaplib exception class and its text.
type imaplibError struct{ kind, msg string }

func (e *imaplibError) Error() string   { return e.msg }
func (e *imaplibError) PyClass() string { return e.kind }

// idler is a client whose IDLE answers come from idle.
type idler struct {
	mail.Client
	idle func(ctx context.Context) (bool, error)
}

func (c idler) IdleChanges(ctx context.Context, _ time.Duration) (bool, error) { return c.idle(ctx) }

// sleeper records the watch loop's waits and ends the loop at the stopAt-th one.
type sleeper struct {
	mu     sync.Mutex
	got    []time.Duration
	stopAt int
	cancel context.CancelFunc
}

func (s *sleeper) sleep(_ context.Context, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	if len(s.got) >= s.stopAt {
		s.cancel()
	}
}

func seconds(ns ...int) []time.Duration {
	out := make([]time.Duration, len(ns))
	for i, n := range ns {
		out[i] = time.Duration(n) * time.Second
	}
	return out
}

// watch runs the icloud watch loop until the stopAt-th wait and returns the waits.
func watch(t *testing.T, w *world, stopAt int) []time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &sleeper{stopAt: stopAt, cancel: cancel}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.watcher(s.sleep).Account(ctx, config.ICloud)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the watch loop did not stop")
	}
	return s.got
}

func TestNextBackoff(t *testing.T) {
	var got []int
	for b := 0; len(got) < 9; {
		b = NextBackoff(b)
		got = append(got, b)
	}
	if want := []int{5, 10, 20, 40, 80, 160, 300, 300, 300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backoffs = %v, want %v", got, want)
	}
}

func TestConnectionLost(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&imaplibError{"abort", "socket error: EOF"}, true},
		{&imaplibError{"readonly", "INBOX is not writable"}, true},
		{&imaplibError{"error", "LOGIN failed"}, false},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{fmt.Errorf("write raw: %w", &os.PathError{Op: "open", Path: "/x", Err: os.ErrPermission}), true},
		{errors.New("missing credentials for icloud"), false},
		{context.Canceled, false},
	} {
		if got := ConnectionLost(tc.err); got != tc.want {
			t.Errorf("ConnectionLost(%v) = %v", tc.err, got)
		}
	}
}

func TestWatchBacksOffOnConnectionLossAndResetsAfterAPass(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	logs := captureLogs(t)
	w.Fail = func(_ string, n int) error {
		if n <= 8 {
			return &imaplibError{"abort", "socket error: EOF"}
		}
		return nil
	}
	idles := 0
	w.Wrap = func(_ string, c mail.Client) mail.Client {
		return idler{c, func(context.Context) (bool, error) {
			idles++
			return false, &imaplibError{"abort", "socket error: connection reset"}
		}}
	}
	got := watch(t, w, 9)
	if want := seconds(5, 10, 20, 40, 80, 160, 300, 300, 5); !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if idles != 1 {
		t.Errorf("IDLE ran %d times", idles)
	}
	logs.has(t, "level=WARN", "atrium.watch", "icloud connection lost (socket error: EOF), reconnecting in 5s")
	logs.has(t, "level=WARN", "icloud connection lost (socket error: EOF), reconnecting in 300s")
	logs.has(t, "level=WARN", "icloud connection lost (socket error: connection reset), reconnecting in 5s")
	if !w.fakes[config.ICloud].Closed {
		t.Error("the client was not closed after the failure")
	}
}

func TestWatchLogsAnyOtherFailureAndBacksOff(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	logs := captureLogs(t)
	w.Fail = func(string, int) error { return errors.New("boom") }
	if got, want := watch(t, w, 3), seconds(5, 10, 20); !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	logs.has(t, "level=ERROR", "atrium.watch", "icloud watch failed, retrying in 5s", "ERR=boom")
	logs.has(t, "level=ERROR", "icloud watch failed, retrying in 20s")
}

func TestWatchWithoutIdleWaitsAndNoops(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	fake := w.fakes[config.ICloud]
	delete(fake.Caps, "IDLE")
	idles := 0
	w.Wrap = func(_ string, c mail.Client) mail.Client {
		return idler{c, func(context.Context) (bool, error) { idles++; return false, nil }}
	}
	if got, want := watch(t, w, 3), []time.Duration{58 * time.Second, 2 * time.Second, 58 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if fake.Noops != 2 || idles != 0 {
		t.Errorf("noops %d, idles %d", fake.Noops, idles)
	}
	if got := w.opens(); !slices.Equal(got[:3], []string{config.ICloud, config.ICloud, config.ICloud}) {
		t.Errorf("opened %v", got)
	}
}

func TestWatchIdlesOnTheInboxAndSettlesAfterAChange(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	fake := w.fakes[config.ICloud]
	fake.Idle = true
	w.arrive(config.ICloud, "Hello", "a@x.test")
	if got, want := watch(t, w, 2), seconds(2, 2); !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if fake.Selected != config.Inbox {
		t.Errorf("selected %q", fake.Selected)
	}
	if n := w.count("SELECT COUNT(*) FROM messages"); n != 1 {
		t.Errorf("messages = %d", n)
	}
}

func TestWatchPassesAfterAnIdleTimeoutWithoutSettling(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idles := 0
	w.Wrap = func(_ string, c mail.Client) mail.Client {
		return idler{c, func(context.Context) (bool, error) {
			idles++
			if idles == 3 {
				cancel()
			}
			return false, nil
		}}
	}
	s := &sleeper{stopAt: 1, cancel: cancel}
	w.watcher(s.sleep).Account(ctx, config.ICloud)
	if len(s.got) != 0 || idles != 3 {
		t.Fatalf("waits %v, idles %d", s.got, idles)
	}
	if got := w.opens(); !slices.Equal(got, []string{config.ICloud, config.ICloud, config.ICloud, config.ICloud}) {
		t.Errorf("opened %v (first pass, IDLE client, two passes)", got)
	}
}

func TestWatchBacksOffWhenTheEmbeddingResponseHasTheWrongShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()
	w := newWorld(t, config.Shadow, credentials)
	w.pass.Cfg.EmbedURL = srv.URL
	logs := captureLogs(t)
	w.arrive(config.ICloud, "Hello", "a@x.test")
	if got, want := watch(t, w, 2), seconds(5, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	logs.has(t, "level=ERROR", "icloud watch failed, retrying in 5s", "embedding response shape mismatch: expected 1x768")
}

func TestWatchFirstPassWaitsForTheDatabaseLock(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.fakes[config.ICloud].Idle = true
	s := &sleeper{stopAt: 1, cancel: cancel}
	lockedOut(t, w, func() { w.watcher(s.sleep).Account(ctx, config.ICloud) },
		func() bool { return len(w.opens()) > 0 })
}

func TestWatchRunsOneLoopPerSelectedAccountUntilCancelled(t *testing.T) {
	w := newWorld(t, config.Shadow, map[string]string{"ICLOUD_MAIL_USERNAME": "u", "ICLOUD_APP_PASSWORD": "p"})
	ctx, cancel := context.WithCancel(context.Background())
	idling := make(chan struct{})
	var once sync.Once
	w.Wrap = func(_ string, c mail.Client) mail.Client {
		return idler{c, func(ctx context.Context) (bool, error) {
			once.Do(func() { close(idling) })
			<-ctx.Done()
			return false, ctx.Err()
		}}
	}
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, w.cfg, "", w.open) }()
	select {
	case <-idling:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop never reached IDLE")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}
	if got := w.opens(); !slices.Equal(got, []string{config.ICloud, config.ICloud}) {
		t.Errorf("opened %v", got)
	}
	err := Watch(context.Background(), w.cfg, config.Gmail, w.open)
	var missing MissingCredentials
	if !errors.As(err, &missing) {
		t.Errorf("Watch(gmail) err = %v", err)
	}
}

// cancelOnCopy ends the watch while the live pass is moving a message.
type cancelOnCopy struct {
	mail.Client
	cancel context.CancelFunc
}

func (c cancelOnCopy) Copy(ctx context.Context, uids mail.UIDSet, dest string) (*mail.CopyUID, error) {
	c.cancel()
	return c.Client.Copy(ctx, uids, dest)
}

func TestShutdownDuringALivePassLetsThePassFinish(t *testing.T) {
	w := newWorld(t, config.Live, credentials)
	if err := w.pass.Locked(context.Background(), context.Background(), []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	w.rule("x.test", "Bills")
	w.arrive(config.ICloud, "Invoice", "a@x.test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Wrap = func(_ string, c mail.Client) mail.Client { return cancelOnCopy{c, cancel} }
	s := &sleeper{stopAt: 100, cancel: cancel}
	w.watcher(s.sleep).Account(ctx, config.ICloud)
	if ctx.Err() == nil || len(s.got) != 0 {
		t.Fatalf("cancelled %v, waits %v", ctx.Err(), s.got)
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE ok = 1 AND kind = 'file'"); n != 1 {
		t.Errorf("finished file actions = %d", n)
	}
	if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE ok IS NULL OR error = ?", live.Interrupted); n != 0 {
		t.Errorf("%d actions held or interrupted", n)
	}
	fake := w.fakes[config.ICloud]
	if len(fake.Boxes["Bills"].UIDs()) != 1 || len(fake.Boxes[config.Inbox].UIDs()) != 0 {
		t.Errorf("Bills %v, INBOX %v", fake.Boxes["Bills"].UIDs(), fake.Boxes[config.Inbox].UIDs())
	}
	if n := w.count("SELECT COUNT(*) FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id WHERE b.name = 'Bills' AND m.gone_at IS NULL"); n != 1 {
		t.Errorf("the pass did not resync after the move: %d live messages in Bills", n)
	}
}

func TestWatchRetriesADatabaseThatCannotBeOpened(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	logs := captureLogs(t)
	w.fakes[config.ICloud].Idle = true
	blocker := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := w.cfg
	cfg.DBPath = filepath.Join(blocker, "atrium.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	sleep := func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
		switch len(waits) {
		case 2:
			if err := os.Remove(blocker); err != nil {
				t.Error(err)
			}
		case 3:
			cancel()
		}
	}
	(&Watcher{Cfg: cfg, Open: w.open, Sleep: sleep}).Account(ctx, config.ICloud)
	if want := seconds(5, 10, 2); !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	logs.has(t, "level=WARN", "icloud connection lost (mkdir "+blocker+": not a directory), reconnecting in 5s")
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Errorf("the database was not created once its directory could be: %v", err)
	}
}
