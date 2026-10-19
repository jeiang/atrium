package run

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/py"
)

func TestSelectAccountsSkipsAccountsWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		creds map[string]string
		want  []string
	}{
		{nil, nil},
		{map[string]string{"GMAIL_ADDRESS": "g", "GMAIL_APP_PASSWORD": "p"}, []string{config.Gmail}},
		{map[string]string{"GMAIL_ADDRESS": "g", "GMAIL_APP_PASSWORD": " "}, nil},
		{credentials, []string{config.ICloud, config.Gmail}},
	} {
		cfg, err := config.Load(config.Map(merge(map[string]string{"ATRIUM_DB": "/tmp/x/atrium.db", "ATRIUM_CACHE_DIR": "/tmp/x/cache"}, tc.creds)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := SelectAccounts(cfg, "")
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SelectAccounts(%v) = %v, %v; want %v", tc.creds, got, err, tc.want)
		}
	}
}

func TestSelectAccountsErrorsForAnExplicitAccountWithoutCredentials(t *testing.T) {
	cfg, err := config.Load(config.Map(map[string]string{"ATRIUM_DB": "/tmp/x/atrium.db", "ATRIUM_CACHE_DIR": "/tmp/x/cache", "ICLOUD_MAIL_USERNAME": "u", "ICLOUD_APP_PASSWORD": "p"}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := SelectAccounts(cfg, config.ICloud)
	if err != nil || !reflect.DeepEqual(got, []string{config.ICloud}) {
		t.Fatalf("SelectAccounts(icloud) = %v, %v", got, err)
	}
	_, err = SelectAccounts(cfg, config.Gmail)
	var missing MissingCredentials
	if !errors.As(err, &missing) || py.ExceptionText(err) != "RuntimeError: missing credentials for gmail" {
		t.Fatalf("SelectAccounts(gmail) err = %v", err)
	}
}

func merge(a, b map[string]string) map[string]string {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func golive(t *testing.T, w *world) *string {
	t.Helper()
	v, err := live.GetState(context.Background(), w.db.Write, live.GoliveKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProcessRecordsBaselineInLiveModeOnly(t *testing.T) {
	for mode, want := range map[string]bool{config.Shadow: false, config.Live: true} {
		w := newWorld(t, mode, nil)
		if err := w.pass.Process(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if got := golive(t, w) != nil; got != want {
			t.Errorf("%s: golive recorded = %v", mode, got)
		}
	}
}

func TestBaselineIsTakenAfterSyncingEveryAccount(t *testing.T) {
	w := newWorld(t, config.Live, credentials)
	w.arrive(config.Gmail, "g-read", "b@y.test")
	if err := w.pass.Process(context.Background(), []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	if got := w.opens(); !reflect.DeepEqual(got, []string{config.ICloud, config.Gmail}) {
		t.Errorf("opened %v", got)
	}
	if n := w.count("SELECT COUNT(*) FROM live_baseline b JOIN messages m ON m.id = b.message_id WHERE m.account = 'gmail'"); n != 1 {
		t.Errorf("gmail messages in the baseline = %d", n)
	}
}

func TestPassOrderIsGoliveRouteFlagEventsApplyExtras(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, config.Live, credentials)
	logs := captureLogs(t)
	if err := w.pass.Locked(ctx, []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	if golive(t, w) == nil {
		t.Fatal("first pass took no baseline")
	}
	w.rule("x.test", "Bills")
	w.arrive(config.ICloud, "Invoice", "a@x.test")
	var order []string
	w.pass.Events = func(_ context.Context, account string) (int, int, error) {
		order = append(order, "events")
		if n := w.count("SELECT COUNT(*) FROM decisions WHERE dest = 'Bills'"); n != 1 {
			t.Errorf("events ran with %d decisions", n)
		}
		if n := w.count("SELECT COUNT(*) FROM auto_actions"); n != 0 {
			t.Errorf("events ran after %d automatic writes", n)
		}
		return 2, 3, nil
	}
	w.pass.Extras = append(w.pass.Extras, Extra{"probe", func(context.Context) (any, error) {
		order = append(order, "extra")
		return py.Dict{{Key: "writes", Value: int64(w.count("SELECT COUNT(*) FROM auto_actions WHERE ok = 1"))}}, nil
	}})
	if err := w.pass.Locked(ctx, []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"events", "extra"}) {
		t.Errorf("order = %v", order)
	}
	if n := len(w.fakes[config.ICloud].Boxes["Bills"].UIDs()); n != 1 {
		t.Errorf("Bills holds %d messages after the pass", n)
	}
	logs.has(t, "atrium.run", "icloud: 1 routed, 0 flag judgments")
	logs.has(t, "atrium.run", "icloud: 2 invitation proposals, 3 event candidates")
	logs.has(t, "atrium.run", "probe: {'writes': 1}")
	logs.has(t, "atrium.run", "open loops: {'backfilled': ")
	logs.has(t, "atrium.run", "watches: {'packages': ")
	logs.has(t, "atrium.run", "icloud: 1 new, 0 gone, 0 flag changes")
}

func TestOneAccountsFailureDoesNotStopTheOthers(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, config.Live, credentials)
	accounts := []string{config.ICloud, config.Gmail}
	if err := w.pass.Locked(ctx, accounts); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	w.rule("x.test", "Bills")
	w.arrive(config.ICloud, "Invoice", "a@x.test")
	w.arrive(config.Gmail, "Receipt", "b@x.test")
	w.Fail = func(account string, n int) error {
		if account == config.ICloud && n == 4 {
			return errors.New("icloud is down")
		}
		return nil
	}
	var scanned []string
	w.pass.Events = func(_ context.Context, account string) (int, int, error) {
		scanned = append(scanned, account)
		if account == config.ICloud {
			return 0, 0, errors.New("bad invite")
		}
		return 0, 0, nil
	}
	var ran []string
	w.pass.Extras = []Extra{
		{"first", func(context.Context) (any, error) { ran = append(ran, "first"); return nil, errors.New("broken") }},
		{"second", func(context.Context) (any, error) { ran = append(ran, "second"); return int64(7), nil }},
	}
	if err := w.pass.Locked(ctx, accounts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scanned, accounts) || !reflect.DeepEqual(ran, []string{"first", "second"}) {
		t.Errorf("scanned %v, extras %v", scanned, ran)
	}
	logs.has(t, "level=ERROR", "icloud: event scan failed", "bad invite")
	logs.has(t, "level=ERROR", "icloud: live pass failed", "icloud is down")
	logs.has(t, "gmail: 1 routed, 0 flag judgments")
	logs.has(t, "level=ERROR", "first refresh failed", "broken")
	logs.has(t, "second: 7")
	if n := w.count("SELECT COUNT(*) FROM auto_actions WHERE account = 'gmail' AND ok = 1"); n != 1 {
		t.Errorf("gmail automatic writes = %d", n)
	}
}

func TestEmbeddingResponseOfTheWrongShapeEndsThePass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()
	w := newWorld(t, config.Shadow, credentials)
	w.pass.Cfg.EmbedURL = srv.URL
	w.arrive(config.ICloud, "Hello", "a@x.test")
	err := w.pass.Locked(context.Background(), []string{config.ICloud})
	if !errors.Is(err, httpx.ErrValue) {
		t.Fatalf("pass err = %v, want a ValueError", err)
	}
	if n := w.count("SELECT COUNT(*) FROM decisions"); n != 0 {
		t.Errorf("%d decisions after the failed embedding", n)
	}
}

func TestEmbedderDownIsLoggedAndThePassGoesOn(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	logs := captureLogs(t)
	w.arrive(config.ICloud, "Hello", "a@x.test")
	if err := w.pass.Locked(context.Background(), []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	logs.has(t, "level=WARN", "atrium.run", "embedding unavailable: no service URL configured for '/embeddings'")
	logs.has(t, "icloud: 0 routed, 0 flag judgments")
}

func TestSyncRunsOneLockedPassOfTheSelectedAccounts(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	w.arrive(config.Gmail, "Hello", "a@x.test")
	accounts, err := Sync(context.Background(), w.cfg, config.Gmail, w.open)
	if err != nil || !reflect.DeepEqual(accounts, []string{config.Gmail}) {
		t.Fatalf("Sync = %v, %v", accounts, err)
	}
	if n := w.count("SELECT COUNT(*) FROM messages WHERE account = 'gmail'"); n != 1 {
		t.Errorf("gmail messages = %d", n)
	}
	if got := w.opens(); !reflect.DeepEqual(got, []string{config.Gmail}) {
		t.Errorf("opened %v", got)
	}
	if _, err := Sync(context.Background(), w.cfg, "", w.open); err != nil {
		t.Fatal(err)
	}
	if got := w.opens(); !reflect.DeepEqual(got, []string{config.Gmail, config.ICloud, config.Gmail}) {
		t.Errorf("opened %v", got)
	}
}

func TestPassCheckpointsTheWAL(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	w.arrive(config.ICloud, "Hello", "a@x.test")
	if err := w.pass.Locked(context.Background(), []string{config.ICloud}); err != nil {
		t.Fatal(err)
	}
	var busy, frames, done int64
	if err := w.db.Write.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &frames, &done); err != nil {
		t.Fatal(err)
	}
	if frames != 0 {
		t.Errorf("WAL holds %d frames after the pass", frames)
	}
}

// lockedOut runs fn while the test holds the database's lock and checks that it opens no account until the lock
// is released.
func lockedOut(t *testing.T, w *world, fn func(), started func() bool) {
	t.Helper()
	lock, err := db.Exclusive(w.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	time.Sleep(300 * time.Millisecond)
	if started() {
		t.Error("the pass ran while another process held the lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass did not run after the lock was released")
	}
	if !started() {
		t.Error("the pass never ran")
	}
}

func TestSyncWaitsForTheDatabaseLock(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	lockedOut(t, w, func() {
		if _, err := Sync(context.Background(), w.cfg, config.ICloud, w.open); err != nil {
			t.Error(err)
		}
	}, func() bool { return len(w.opens()) > 0 })
}

// TestSyncWatchAndCorrectionsSerialiseOnOneLock runs a sync pass, a watch loop and the web correction move (which
// takes db.Exclusive on the same database) together: no two are ever inside the lock at once.
func TestSyncWatchAndCorrectionsSerialiseOnOneLock(t *testing.T) {
	w := newWorld(t, config.Shadow, credentials)
	w.fakes[config.ICloud].Idle = true
	var inside, overlaps, entries atomic.Int32
	enter := func() {
		entries.Add(1)
		if inside.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(30 * time.Millisecond)
		inside.Add(-1)
	}
	w.pass.Events = func(context.Context, string) (int, int, error) {
		enter()
		return 0, 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sleeper{stopAt: 3, cancel: cancel}
	var wg sync.WaitGroup
	wg.Go(func() { w.watcher(s.sleep).Account(ctx, config.ICloud) })
	for range 3 {
		wg.Go(func() {
			if err := w.pass.Locked(context.Background(), []string{config.Gmail}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			lock, err := db.Exclusive(w.cfg.DBPath)
			if err != nil {
				t.Error(err)
				return
			}
			enter()
			_ = lock.Close()
		})
	}
	wg.Wait()
	if overlaps.Load() != 0 || entries.Load() < 8 {
		t.Fatalf("%d of %d lock holders overlapped", overlaps.Load(), entries.Load())
	}
}
