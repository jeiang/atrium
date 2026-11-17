//go:build !modernc

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/apps/mail/mailtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
)

func runtimeRow(t *testing.T, dir, id string) (label, reason string, checked float64) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if err := d.Read.QueryRow("SELECT label, reason, COALESCE(checked_at, 0) FROM connections WHERE id = ?", id).Scan(&label, &reason, &checked); err != nil {
		t.Fatal(err)
	}
	return label, reason, checked
}

func TestMailRuntimeFailuresFlipTheConnectionOnceAndASuccessRestoresIt(t *testing.T) {
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@home.test", "app-pass", false}))
	fake := mailtest.NewFakeClient()
	inbox := fake.AddBox("INBOX", mailtest.NewBox(1))
	h, x := mailtest.RawWith("Hello", "friend@x.test", "hello-1", "Hi there.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	var refuse atomic.Bool
	t.Cleanup(imapclient.UseDialer(func(_ context.Context, a config.AccountSpec, password string) (mail.Conn, error) {
		if refuse.Load() {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		if a.Login != "me@home.test" || password != "app-pass" {
			return nil, errors.New("AUTHENTICATIONFAILED Invalid credentials")
		}
		return fake.Conn(), nil
	}))
	smtp := strictSMTPRig(t, map[string]string{"me@home.test": "app-pass"})

	base, _ := startServeLogged(t, dir)
	token := localAPIToken(t, "api")
	s := &apiSite{t: t, base: base}
	waitUntil(t, 30*time.Second, "the boot check", func() bool { return connectionState(t, dir, "mail/home") == "ok" })
	label, _, checked := runtimeRow(t, dir, "mail/home")
	notices := connectionNotices(t, dir, label)
	sync := func() { s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{"account": "home"}) }

	sync()
	time.Sleep(500 * time.Millisecond)
	if _, _, again := runtimeRow(t, dir, "mail/home"); again != checked || connectionState(t, dir, "mail/home") != "ok" {
		t.Fatalf("a working sync rewrote the connection: checked %v then %v", checked, again)
	}

	refuse.Store(true)
	sync()
	waitUntil(t, 10*time.Second, "the refused session to fail the connection", func() bool { return connectionState(t, dir, "mail/home") == "failing" })
	if _, reason, _ := runtimeRow(t, dir, "mail/home"); !strings.Contains(reason, "connection refused") {
		t.Fatalf("failing reason %q lacks the refusal", reason)
	}
	sync()
	time.Sleep(500 * time.Millisecond)
	if n := connectionNotices(t, dir, label) - notices; n != 1 {
		t.Fatalf("%d notices after two refused syncs, want 1", n)
	}

	refuse.Store(false)
	sync()
	waitUntil(t, 10*time.Second, "a working session to restore the connection", func() bool { return connectionState(t, dir, "mail/home") == "ok" })
	if n := connectionNotices(t, dir, label) - notices; n != 2 {
		t.Fatalf("%d notices after the recovery, want 2", n)
	}

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	send := func(subject string) apiReply {
		t.Helper()
		if _, err := d.Write.Exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, revision, created_at, updated_at) VALUES (NULL, 'home', 'friend@x.test', ?, 'See you.', 'draft', 1, 1000, 1000)", subject); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := d.Read.QueryRow("SELECT CAST(id AS TEXT) FROM drafts WHERE subject = ?", subject).Scan(&id); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/drafts/" + id
		rev := number(t, s.do(http.MethodGet, path, token, nil).json["revision"])
		return s.do(http.MethodPost, path+"/send", token, map[string]any{"revision": rev})
	}
	smtp.setPassword("me@home.test", "rotated")
	if sent := send("Refused"); sent.status == http.StatusOK {
		t.Fatalf("a refused login sent the draft: %s", sent.body)
	}
	waitUntil(t, 10*time.Second, "the refused login to need reauth", func() bool { return connectionState(t, dir, "mail/home") == "needs_reauth" })
	if n := connectionNotices(t, dir, label) - notices; n != 2 {
		t.Fatalf("%d notices after a refused login within the damping window, want 2", n)
	}
	smtp.setPassword("me@home.test", "app-pass")
	if sent := send("Accepted"); sent.status != http.StatusOK {
		var result string
		_ = d.Read.QueryRow("SELECT COALESCE(smtp_result, '') FROM drafts WHERE subject = 'Accepted'").Scan(&result)
		t.Fatalf("send after the password returned: %d %s %s", sent.status, sent.body, result)
	}
	waitUntil(t, 10*time.Second, "a working send to restore the connection", func() bool { return connectionState(t, dir, "mail/home") == "ok" })
	if n := connectionNotices(t, dir, label) - notices; n != 2 {
		t.Fatalf("%d notices after a recovery from a held failure, want 2", n)
	}
}

func TestDAVMirrorFailuresFlipTheICloudConnectionAndASyncRestoresIt(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, srv := davFixture(t)
	host := accountsFile(t, dir, mailAccount{"icloud", "icloud", "operator@icloud.invalid", davPassword, false})
	f, err := os.OpenFile(host, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, `
[connections."icloud/icloud"]
kind = "icloud"
label = "iCloud DAV"
config = { apple_id = "operator@icloud.invalid" }
capabilities = ["caldav", "carddav"]
credential_files = { "mail/icloud/password" = %q }
`, filepath.Join(dir, "icloud.password")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	strictIMAPRig(t, map[string]string{"operator@icloud.invalid": davPassword}, map[string]*mailtest.FakeClient{"icloud": fake})
	strictSMTPRig(t, map[string]string{"operator@icloud.invalid": davPassword})
	base, _ := startServeLogged(t, dir)
	token := localAPIToken(t, "api")
	s := &apiSite{t: t, base: base}
	id := "icloud/icloud"
	if got := runApp(t, "connections", "test", id); got.code != cli.ExitOK {
		t.Fatalf("connections test %s: exit %d stdout %q stderr %q", id, got.code, got.stdout, got.stderr)
	}
	label, _, _ := runtimeRow(t, dir, id)
	notices := connectionNotices(t, dir, label)

	srv.Unavailable.Store(true)
	s.do(http.MethodPost, "/api/v1/contacts/sync", token, nil)
	waitUntil(t, 10*time.Second, "the unavailable server to fail the connection", func() bool { return connectionState(t, dir, id) == "failing" })
	s.do(http.MethodPost, "/api/v1/calendar/sync", token, nil)
	time.Sleep(500 * time.Millisecond)
	if n := connectionNotices(t, dir, label) - notices; n != 1 {
		t.Fatalf("%d notices after two failed syncs, want 1", n)
	}

	srv.Unavailable.Store(false)
	for _, path := range []string{"/api/v1/contacts/sync", "/api/v1/calendar/sync"} {
		if got := s.do(http.MethodPost, path, token, nil); got.status != http.StatusOK {
			t.Fatalf("%s after the server returned: %d %s", path, got.status, got.body)
		}
	}
	waitUntil(t, 10*time.Second, "working syncs to restore the connection", func() bool { return connectionState(t, dir, id) == "ok" })
	if n := connectionNotices(t, dir, label) - notices; n != 2 {
		t.Fatalf("%d notices after the recovery, want 2", n)
	}
}

func TestConnectionsTestAllChecksSilentlyAndNeedsAnIDOrAll(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "connections", "list"); ran.code != cli.ExitOK {
		t.Fatalf("connections list: exit %d stderr %q", ran.code, ran.stderr)
	}
	browser.store(t, mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t)))

	all := runApp(t, "connections", "test", "--all", "--json")
	if all.code != cli.ExitOK && all.code != cli.ExitError || !strings.Contains(all.stdout, "service/webpush") {
		t.Fatalf("connections test --all: exit %d stdout %q stderr %q", all.code, all.stdout, all.stderr)
	}
	if n := push.count(); n != 0 {
		t.Fatalf("connections test --all sent %d pushes, want 0", n)
	}
	for _, args := range [][]string{{"connections", "test"}, {"connections", "test", "service/webpush", "--all"}} {
		if got := runApp(t, args...); got.code != cli.ExitUsage {
			t.Fatalf("%v: exit %d stderr %q, want a usage error", args, got.code, got.stderr)
		}
	}
	if one := runApp(t, "connections", "test", "service/webpush"); one.code != cli.ExitOK || push.count() != 1 {
		t.Fatalf("connections test service/webpush: exit %d, %d pushes", one.code, push.count())
	}
}

func refusingIMAP(t *testing.T) {
	t.Helper()
	ca := smtpCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", smtpTLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = c.Write([]byte("* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] ready\r\n"))
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					tag, command, _ := strings.Cut(strings.TrimSpace(line), " ")
					switch verb, _, _ := strings.Cut(strings.ToUpper(command), " "); verb {
					case "LOGIN", "AUTHENTICATE":
						_, _ = c.Write([]byte(tag + " NO [AUTHENTICATIONFAILED] Authentication failed.\r\n"))
					case "LOGOUT":
						_, _ = c.Write([]byte("* BYE\r\n" + tag + " OK\r\n"))
						return
					default:
						_, _ = c.Write([]byte(tag + " OK\r\n"))
					}
				}
			}()
		}
	}()
	t.Setenv("ATRIUM_IMAP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_IMAP_CA", ca)
}

func TestARefusedIMAPLoginNeedsReauthOnceAtRuntimeAndInTheCheck(t *testing.T) {
	dir, embed := keyedMailFixture(t)
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed, filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"home", "icloud", "me@home.test", "app-pass", false}))
	refusingIMAP(t)
	strictSMTPRig(t, map[string]string{"me@home.test": "app-pass"})
	if got := runApp(t, "connections", "list"); got.code != cli.ExitOK {
		t.Fatalf("connections list: exit %d stderr %q", got.code, got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec(`UPDATE connections SET state = 'ok', reason = 'seeded', checked_at = ?, results_json = '[{"capability":"imap","state":"ok"},{"capability":"smtp","state":"ok"}]' WHERE id = 'mail/home'`, float64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	label, _, _ := runtimeRow(t, dir, "mail/home")
	notices := connectionNotices(t, dir, label)

	base, _ := startServeLogged(t, dir)
	token := localAPIToken(t, "api")
	s := &apiSite{t: t, base: base}
	s.do(http.MethodPost, "/api/v1/mail/sync", token, map[string]any{"account": "home"})
	waitUntil(t, 10*time.Second, "the refused login to need reauth", func() bool { return connectionState(t, dir, "mail/home") == "needs_reauth" })
	if got := runApp(t, "connections", "test", "mail/home"); !strings.Contains(got.stdout, "mail/home\tneeds_reauth") {
		t.Fatalf("connections test mail/home: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	if n := connectionNotices(t, dir, label) - notices; n != 1 {
		t.Fatalf("%d notices for one refused login, want 1", n)
	}
}
