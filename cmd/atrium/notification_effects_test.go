package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
)

type notifyPush struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	drop     bool
	block    bool
	received int
	arrived  chan struct{}
}

func newNotifyPush(t *testing.T) *notifyPush {
	t.Helper()
	p := &notifyPush{status: http.StatusCreated, arrived: make(chan struct{}, 16)}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		p.mu.Lock()
		p.received++
		status, drop, block := p.status, p.drop, p.block
		p.mu.Unlock()
		select {
		case p.arrived <- struct{}{}:
		default:
		}
		if block {
			<-r.Context().Done()
			return
		}
		if drop {
			if hijack, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijack.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *notifyPush) answer(status int, drop, block bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.drop, p.block = status, drop, block
}

func (p *notifyPush) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.received
}

func notifyPushRig(t *testing.T, dir string) (*notifyPush, pushBrowser) {
	t.Helper()
	push := newNotifyPush(t)
	ca := filepath.Join(dir, "push-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: push.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, settingEnv(notify.KeyPushSubject), "mailto:ops@example.test")
	setEnv(t, settingEnv(transport.CAKey(notify.PushTransport)), ca)
	allowLoopbackPush.Do(func() { notify.UsePushAddresses(func(a netip.Addr) bool { return a.IsLoopback() }) })
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	browser := pushBrowser{key: key, secret: make([]byte, 16), endpoint: push.URL + "/push/device-1"}
	_, _ = rand.Read(browser.secret)
	return push, browser
}

func notifyCounts(t *testing.T, d *db.DB) (notifications, effects int) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM effects WHERE operation = 'notify_inbox' AND direction = 'forward'").Scan(&effects); err != nil {
		t.Fatal(err)
	}
	return notifications, effects
}

func dueReminder(t *testing.T, d *db.DB, title string, at float64) {
	t.Helper()
	if _, err := d.Write.Exec("INSERT INTO tasks (title, notes, status, remind_at, created_ts, updated_ts) VALUES (?, '', 'open', ?, ?, ?)", title, at, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationInboxEffectIsDurableAndDeduped(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := notifyPushRig(t, dir)
	push.answer(http.StatusInternalServerError, false, false)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	browser.store(t, d)
	now := float64(time.Now().Unix())
	dueReminder(t, d, "Pay the rent", now-1)
	runJob(t, f, d, "task-reminders")

	notifications, effects := notifyCounts(t, d)
	if notifications != 1 || effects != 1 {
		t.Fatalf("after a failed push: %d notifications, %d effects; want one of each", notifications, effects)
	}
	var state, origin string
	if err := d.Read.QueryRow("SELECT state, origin FROM effects WHERE operation = 'notify_inbox'").Scan(&state, &origin); err != nil {
		t.Fatal(err)
	}
	if state != "done" || origin != "notify" {
		t.Fatalf("inbox effect = %s/%s; want done/notify", origin, state)
	}
	var id int64
	var delivered bool
	var failure string
	if err := d.Read.QueryRow("SELECT id, delivered, COALESCE(error, '') FROM notifications").Scan(&id, &delivered, &failure); err != nil {
		t.Fatal(err)
	}
	if delivered || !strings.Contains(failure, "web push 500") {
		t.Fatalf("failed push delivered = %v error %q; want undelivered with the transport failure", delivered, failure)
	}
	var reminded *float64
	if err := d.Read.QueryRow("SELECT reminded_ts FROM tasks").Scan(&reminded); err != nil {
		t.Fatal(err)
	}
	if reminded == nil {
		t.Fatal("the reminder marker was not written with the inbox row")
	}

	if _, err := d.Write.Exec("UPDATE tasks SET reminded_ts = NULL"); err != nil {
		t.Fatal(err)
	}
	pushes := push.count()
	runJob(t, f, d, "task-reminders")
	if notifications, effects = notifyCounts(t, d); notifications != 1 || effects != 1 {
		t.Fatalf("repeating the reminder occurrence: %d notifications, %d effects; want one of each", notifications, effects)
	}
	if push.count() != pushes {
		t.Fatalf("a duplicate occurrence pushed again: %d requests", push.count()-pushes)
	}

	push.answer(http.StatusCreated, false, false)
	base, _, stop := startServeStoppable(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if code, body, _ := fetch(t, client, http.MethodGet, base+"/inbox", nil); code != http.StatusOK {
		t.Fatalf("inbox: %d %.300s", code, body)
	}
	if code, body, _ := fetch(t, client, http.MethodPost, fmt.Sprintf("%s/inbox/%d/resend", base, id), url.Values{}); code != http.StatusOK {
		t.Fatalf("resend: %d %s", code, body)
	}
	stop()
	if err := d.Read.QueryRow("SELECT delivered FROM notifications WHERE id = ?", id).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("resent row delivered = %v, %v", delivered, err)
	}
	if notifications, effects = notifyCounts(t, d); notifications != 1 || effects != 1 {
		t.Fatalf("resend created a second inbox effect: %d notifications, %d effects", notifications, effects)
	}
	if push.count() != pushes+1 {
		t.Fatalf("resend pushed %d times, want once", push.count()-pushes)
	}
}

func TestNotificationLostPushResponseLeavesTheInboxDone(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := notifyPushRig(t, dir)
	push.answer(http.StatusCreated, true, false)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	browser.store(t, d)
	now := float64(time.Now().Unix())
	dueReminder(t, d, "Collect the parcel", now-1)
	runJob(t, f, d, "task-reminders")

	notifications, effects := notifyCounts(t, d)
	if notifications != 1 || effects != 1 {
		t.Fatalf("after a dropped push response: %d notifications, %d effects; want one of each", notifications, effects)
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE operation = 'notify_inbox'").Scan(&state); err != nil || state != "done" {
		t.Fatalf("inbox effect = %q, %v; want done", state, err)
	}
	var delivered bool
	var failure string
	if err := d.Read.QueryRow("SELECT delivered, COALESCE(error, '') FROM notifications").Scan(&delivered, &failure); err != nil {
		t.Fatal(err)
	}
	if delivered || !strings.Contains(failure, "web push unreachable") {
		t.Fatalf("dropped response delivered = %v error %q; want the transport failure visible", delivered, failure)
	}
}

func TestNotificationCancelledPushKeepsTheInboxEffect(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := notifyPushRig(t, dir)
	push.answer(http.StatusCreated, false, false)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	browser.store(t, d)
	runJob(t, f, d, "task-reminders")

	push.answer(http.StatusCreated, false, true)
	now := float64(time.Now().Unix())
	dueReminder(t, d, "Call the plumber", now-1)
	if _, err := d.Write.Exec("UPDATE jobs SET state = 'active', next_due = ?, lease_owner = NULL, lease_until = NULL WHERE name = 'task-reminders'", now-1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "scheduler") }()
	select {
	case <-push.arrived:
	case <-time.After(10 * time.Second):
		cancel()
		ran := <-done
		t.Fatalf("no push was attempted: exit %d stderr %s", ran.code, ran.stderr)
	}
	cancel()
	<-done

	notifications, effects := notifyCounts(t, d)
	if notifications != 1 || effects != 1 {
		t.Fatalf("after a cancelled push: %d notifications, %d effects; want one of each", notifications, effects)
	}
	var state string
	if err := d.Read.QueryRow("SELECT state FROM effects WHERE operation = 'notify_inbox'").Scan(&state); err != nil || state != "done" {
		t.Fatalf("inbox effect = %q, %v; want done", state, err)
	}
	var reminded *float64
	if err := d.Read.QueryRow("SELECT reminded_ts FROM tasks").Scan(&reminded); err != nil {
		t.Fatal(err)
	}
	if reminded == nil {
		t.Fatal("the cancelled push lost the reminder marker")
	}
}

func TestNotificationLocalCommitFailureCreatesNothing(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	push, browser := notifyPushRig(t, dir)
	push.answer(http.StatusCreated, false, false)
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	d := mustConnect(t, dir+"/atrium.db")
	browser.store(t, d)
	if _, err := d.Write.Exec("CREATE TRIGGER notify_boom BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT, 'boom'); END"); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	dueReminder(t, d, "Water the plants", now-1)
	runJob(t, f, d, "task-reminders")

	if notifications, effects := notifyCounts(t, d); notifications != 0 || effects != 0 {
		t.Fatalf("a failed local commit left %d notifications, %d effects; want none", notifications, effects)
	}
	var reminded *float64
	if err := d.Read.QueryRow("SELECT reminded_ts FROM tasks").Scan(&reminded); err != nil {
		t.Fatal(err)
	}
	if reminded != nil {
		t.Fatal("a failed local commit wrote the reminder marker")
	}
	if push.count() != 0 {
		t.Fatalf("a failed local commit pushed %d notifications", push.count())
	}

	if _, err := d.Write.Exec("DROP TRIGGER notify_boom"); err != nil {
		t.Fatal(err)
	}
	runJob(t, f, d, "task-reminders")
	if notifications, effects := notifyCounts(t, d); notifications != 1 || effects != 1 {
		t.Fatalf("after the failure cleared: %d notifications, %d effects; want one of each", notifications, effects)
	}
}
