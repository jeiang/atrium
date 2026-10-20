package imapclient

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	maillive "github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

const runList = `C: {t} LIST "" "*"
S: * LIST (\HasNoChildren) "/" INBOX
S: * LIST (\HasNoChildren) "/" "Bills"
S: {t} OK LIST completed
`

func runFetch(seq, uid int, flags, subject, msgid string) string {
	header := "From: a@x.test\r\nSubject: " + subject + "\r\nMessage-ID: <" + msgid + ">\r\n"
	return fmt.Sprintf(fullFetch+"\n", fmt.Sprint(uid)) +
		fmt.Sprintf("S: * %d FETCH (UID %d FLAGS (%s) INTERNALDATE \" 6-Oct-2026 10:00:00 +0000\" RFC822.SIZE 60 "+
			`BODYSTRUCTURE ("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 4 1 NIL NIL NIL NIL) BODY[HEADER] {%d}`+"\n",
			seq, uid, flags, len(header)) +
		"S: From: a@x.test\nS: Subject: " + subject + "\nS: Message-ID: <" + msgid + ">\nS:  BODY[TEXT]<0> {4}\nS: body)\nS: {t} OK FETCH completed\n"
}

func runFlags(since int, lines ...string) string {
	return fmt.Sprintf(flagFetch, since) + "\n" + strings.Join(lines, "\n") + "\nS: {t} OK FETCH completed\n"
}

type recorder struct {
	mu     sync.Mutex
	waits  []time.Duration
	stopAt int
	cancel context.CancelFunc
}

func (r *recorder) sleep(_ context.Context, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waits = append(r.waits, d)
	if len(r.waits) == r.stopAt {
		r.cancel()
	}
}

func TestWatchPassAndIdleCyclesWire(t *testing.T) {
	script := icloudLogin + runList +
		syncStatus("Bills", 0, 1, 5, 1) + syncExamine("Bills", 0, 5) + syncSearch("ALL", "") +
		syncStatus("INBOX", 1, 302, 7, 5) + syncExamine("INBOX", 1, 7) + syncSearch("ALL", "301") +
		runFetch(1, 301, `\Seen`, "Inv", "m1@x.test") +
		logout + "! accept\n" +
		icloudLogin +
		`C/ ^{t} STATUS INBOX \((UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)( (UIDVALIDITY|UIDNEXT|MESSAGES|HIGHESTMODSEQ)){3}\)$
S: * STATUS INBOX (MESSAGES 1 UIDNEXT 302 UIDVALIDITY 7 HIGHESTMODSEQ 5)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 301
S: * SEARCH 301
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 301 \((UID|FLAGS|BODY\.PEEK\[HEADER\])( (UID|FLAGS|BODY\.PEEK\[HEADER\])){2}\)$
S: * 1 FETCH (UID 301 FLAGS (\Seen) BODY[HEADER] {25}
S: Message-ID: <m1@x.test>
S: )
S: {t} OK FETCH completed
` + runList + runList + `C: {t} SELECT INBOX
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 301 "Bills"
S: {t} OK [COPYUID 5 301 1] Done
C: {t} UID STORE 301 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 301
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
` + logout + "! accept\n" +
		icloudLogin + runList +
		syncStatus("Bills", 1, 2, 5, 2) + syncExamine("Bills", 1, 5) + syncSearch("ALL", "1") +
		runFetch(1, 1, `\Seen`, "Inv", "m1@x.test") +
		syncStatus("INBOX", 0, 302, 7, 6) + syncExamine("INBOX", 0, 7) + syncSearch("ALL", "") +
		logout + "! accept\n" +
		icloudLogin + `C: {t} EXAMINE INBOX
S: * 0 EXISTS
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
! sleep 20ms
S: * OK Still here
S: * 1 EXISTS
C: DONE
S: {t} OK IDLE terminated
C: {t} EXAMINE INBOX
S: * 1 EXISTS
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
S: * BYE Server shutting down
! close
! accept
` + icloudLogin + runList +
		syncStatus("Bills", 1, 2, 5, 2) + syncExamine("Bills", 1, 5) + syncSearch("ALL", "1") +
		syncStatus("INBOX", 1, 303, 7, 7) + syncExamine("INBOX", 1, 7) + syncSearch("ALL", "302") +
		runFetch(1, 302, "", "Hey", "m2@x.test") +
		logout
	srv := imapscript.Serve(t, imapscript.MustParse(t, script))

	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_MODE": "live", "ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
		"ICLOUD_MAIL_USERNAME": "atrium.test@icloud.com", "ICLOUD_APP_PASSWORD": "app-specific password",
	})
	cfg, err := config.Load(env)
	must(t, err)
	d, err := db.Connect(cfg.DBPath)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	must(t, db.Migrate(context.Background(), d, cfg.DBPath))
	must(t, maillive.SetState(context.Background(), d.Write, maillive.GoliveKey, "1.0"))
	_, err = d.Write.Exec("INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (0, 'inv', NULL, 'file', 'Bills', ?)",
		`[{"field": "subject", "op": "eq", "value": "Inv"}]`)
	must(t, err)

	open := func(ctx context.Context, _ string) (mail.Client, error) {
		c := newConn("imap.test", "atrium.test@icloud.com", "app-specific password")
		c.dial = func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", srv.Addr())
		}
		c.readTimeout = 5 * time.Second
		client := mail.Gated(c, env)
		if err := client.Connect(ctx); err != nil {
			return nil, err
		}
		return client, nil
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logs.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &recorder{stopAt: 2, cancel: cancel}
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&run.Watcher{Cfg: cfg, Open: open, Sleep: clock.sleep}).Account(ctx, config.ICloud)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the watch loop did not stop")
	}

	if want := []time.Duration{2 * time.Second, 5 * time.Second}; fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", clock.waits, want)
	}
	rows, err := d.Read.Query("SELECT m.msgid, b.name, m.gone_at IS NULL FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id ORDER BY m.id")
	must(t, err)
	var got []string
	for rows.Next() {
		var msgid, box string
		var present bool
		must(t, rows.Scan(&msgid, &box, &present))
		got = append(got, fmt.Sprintf("%s %s %v", msgid, box, present))
	}
	must(t, rows.Err())
	if want := []string{"m1@x.test INBOX false", "m1@x.test Bills true", "m2@x.test INBOX true"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("messages = %q, want %q", got, want)
	}
	var ok int
	must(t, d.Read.QueryRow("SELECT COUNT(*) FROM auto_actions WHERE kind = 'file' AND ok = 1 AND dest = 'Bills'").Scan(&ok))
	if ok != 1 {
		t.Errorf("filed actions = %d", ok)
	}
	logMu.Lock()
	text := logs.String()
	logMu.Unlock()
	for _, want := range []string{
		"icloud: 1 new, 0 gone, 0 flag changes",
		"icloud: 1 routed, 0 flag judgments",
		"icloud: live pass applied={'file': 1}",
		"icloud: 0 routed, 1 flag judgments",
		"icloud connection lost (",
		"reconnecting in 5s",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("logs lack %q:\n%s", want, text)
		}
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func watchEmptyPass() string {
	return icloudLogin + runList +
		syncStatus("Bills", 0, 1, 5, 1) + syncExamine("Bills", 0, 5) + syncSearch("ALL", "") +
		syncStatus("INBOX", 0, 1, 7, 5) + syncExamine("INBOX", 0, 7) + syncSearch("ALL", "") +
		logout
}

func startWireWatch(t *testing.T, script string, ctx context.Context, sleep func(context.Context, time.Duration), afterConnect func(int)) <-chan struct{} {
	t.Helper()
	srv := imapscript.Serve(t, imapscript.MustParse(t, script))
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_MODE": "live", "ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"),
		"ICLOUD_MAIL_USERNAME": "atrium.test@icloud.com", "ICLOUD_APP_PASSWORD": "app-specific password",
	})
	cfg, err := config.Load(env)
	must(t, err)
	d, err := db.Connect(cfg.DBPath)
	must(t, err)
	t.Cleanup(func() { _ = d.Close() })
	must(t, db.Migrate(context.Background(), d, cfg.DBPath))
	must(t, maillive.SetState(context.Background(), d.Write, maillive.GoliveKey, "1.0"))
	opened := 0
	open := func(ctx context.Context, _ string) (mail.Client, error) {
		c := newConn("imap.test", "atrium.test@icloud.com", "app-specific password")
		c.dial = func(ctx context.Context) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", srv.Addr())
		}
		c.readTimeout = 5 * time.Second
		client := mail.Gated(c, env)
		if err := client.Connect(ctx); err != nil {
			return nil, err
		}
		opened++
		if afterConnect != nil {
			afterConnect(opened)
		}
		return client, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&run.Watcher{Cfg: cfg, Open: open, Sleep: sleep, Notify: sdnotify.FromEnv()}).Account(ctx, config.ICloud)
	}()
	return done
}

func TestWatchReconnectsAfterDropDuringIdle(t *testing.T) {
	notify := sdnotifytest.Listen(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &recorder{stopAt: -1}
	script := watchEmptyPass() + "! accept\n" +
		icloudLogin + `C: {t} EXAMINE INBOX
S: * 0 EXISTS
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
! close
! accept
` + watchEmptyPass() + "! accept\n" +
		icloudLogin + `C: {t} LOGOUT
S: * BYE Logging out
S: {t} OK LOGOUT completed
`
	done := startWireWatch(t, script, ctx, clock.sleep, func(opened int) {
		if opened == 4 {
			cancel()
		}
	})
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the watch loop did not stop")
	}
	if want := []time.Duration{5 * time.Second}; fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
	notify.Wait("STATUS=icloud: reconnecting in 5s (connection lost)")
	watchingCount := func() int {
		n := 0
		for _, message := range notify.Messages() {
			if message == "STATUS=watching" {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for watchingCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := watchingCount(); n < 3 {
		t.Fatalf("watching status count %d in %q", n, notify.Messages())
	}
}

func TestWatchBackoffResetsAfterAGoodPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &recorder{stopAt: 4, cancel: cancel}
	drop := `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-specific password"
! close
`
	script := drop + "! accept\n" + drop + "! accept\n" + drop + "! accept\n" +
		watchEmptyPass() + "! accept\n" +
		icloudLogin + `C: {t} EXAMINE INBOX
S: * 0 EXISTS
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} IDLE
S: + idling
! close
`
	done := startWireWatch(t, script, ctx, clock.sleep, nil)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the watch loop did not stop")
	}
	if want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 5 * time.Second}; fmt.Sprint(clock.waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
}

func TestIdleIsNotReissuedWithinTheWatchLoopsIdle(t *testing.T) {
	if got := newConn("imap.test", "u", "p").idleRestart; got <= config.IdleSeconds*time.Second {
		t.Fatalf("go-imap re-issues IDLE after %v, within imaplib's single %ds IDLE", got, config.IdleSeconds)
	}
}
