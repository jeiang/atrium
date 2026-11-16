package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/module"
)

func connectionRow(t *testing.T, dir, id string) (source string, enabled int, state, configJSON string) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	if err := d.Read.QueryRow("SELECT config_source, enabled, state, config_json FROM connections WHERE id = ?", id).
		Scan(&source, &enabled, &state, &configJSON); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return source, enabled, state, configJSON
}

func TestAUIAAddedMailAccountStaysOperatorOwnedAcrossBoots(t *testing.T) {
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	host := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(host, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	fake := mailtest.NewFakeClient()
	fake.AddBox("INBOX", mailtest.NewBox(1))
	t.Cleanup(imapclient.UseDialer(func(context.Context, config.AccountSpec, string) (mail.Conn, error) {
		return fake.Conn(), nil
	}))
	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	smtp := &smtpFake{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go smtp.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)

	s := &connectionSite{t: t, dir: dir}
	s.start()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, _, _ := fetch(t, client, http.MethodGet, s.base+"/settings/mail", nil); code != http.StatusOK {
		t.Fatalf("mail settings: %d", code)
	}
	add := url.Values{"id": {"work"}, "label": {"Work"}, "preset": {"gmail"}, "login": {"me@work.test"}, "password": {"work-pass"}}
	if code, body, _ := fetch(t, client, http.MethodPost, s.base+"/settings/mail/accounts", add); code != http.StatusOK {
		t.Fatalf("add: %d %.300s", code, body)
	}
	s.stop()
	s.start()

	source, enabled, state, configJSON := connectionRow(t, dir, "mail/work")
	if source != "operator" || enabled != 1 || state == "disabled" {
		t.Errorf("the UI mail account is %s/%d/%s after a second boot", source, enabled, state)
	}
	if !strings.Contains(configJSON, "me@work.test") {
		t.Errorf("the UI mail account config changed: %s", configJSON)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var password string
	if err := d.Read.QueryRow("SELECT value FROM connection_credentials WHERE connection_id = 'mail/work' AND slot = 'mail/work/password'").Scan(&password); err != nil {
		t.Fatalf("the UI mail account password: %v", err)
	}
	_ = d.Close()
	if password != "work-pass" {
		t.Error("the UI mail account password changed")
	}
	tested := runApp(t, "connections", "test", "mail/work")
	if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
		t.Errorf("connections test mail/work: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
}

func TestARemovedHostConnectionStaysDisabled(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	host := filepath.Join(dir, "host.toml")
	write := func(body string) {
		if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`[connections."service/qbittorrent"]
kind = "service/qbittorrent"
label = "qBittorrent"
config = { url = "http://127.0.0.1:9" }
capabilities = ["api"]
`)
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	s := &connectionSite{t: t, dir: dir}
	s.start()
	if source, _, _, _ := connectionRow(t, dir, "service/qbittorrent"); source != "host" {
		t.Fatalf("the declared connection is %s, not host", source)
	}
	s.stop()
	write("")
	s.start()

	if _, enabled, state, _ := connectionRow(t, dir, "service/qbittorrent"); enabled != 0 || state != "disabled" {
		t.Fatalf("the removed connection is enabled=%d state=%s", enabled, state)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	_, err := connections.OpenRead(context.Background(), d.Read, config.Host{}, connections.ID("service/qbittorrent"))
	var disabled connections.DisabledConnection
	if !errors.As(err, &disabled) {
		t.Errorf("a disabled connection opened: %v", err)
	}
}

func TestARemovedHostBuiltinStaysDisabledAndNeverReachesItsEndpoint(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	embed := newFakeOpenAI(t)
	dir, _, _ := moduleFixture(t, flagsExcept(module.Mail, ""))
	unsetEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL")
	plain := filepath.Join(dir, "host-plain.toml")
	if err := os.WriteFile(plain, []byte(hostConfigBodyWith("", defaultWebPushConnection, filepath.Join(testRoot, "host", "anthropic.key"))), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHostWith(t, dir, embed.URL+"/v1", "embeddinggemma", "", "embedding-secret"))
	requests := func() int {
		n := 0
		for _, seen := range embed.authorizations() {
			n += len(seen)
		}
		return n
	}

	s := &connectionSite{t: t, dir: dir}
	s.start()
	if source, enabled, _, _ := connectionRow(t, dir, "service/embedding"); source != "host" || enabled != 1 {
		t.Fatalf("the declared built-in is %s/%d, not host/enabled", source, enabled)
	}
	s.stop()
	before := requests()
	setEnv(t, "ATRIUM_HOST_CONFIG", plain)
	s.start()
	s.stop()
	s.start()
	defer s.stop()

	if source, enabled, state, _ := connectionRow(t, dir, "service/embedding"); source != "host" || enabled != 0 || state != "disabled" {
		t.Fatalf("the removed built-in is %s/%d/%s", source, enabled, state)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	_, err := connections.OpenRead(context.Background(), d.Read, config.Host{}, connections.ID("service/embedding"))
	var disabled connections.DisabledConnection
	if !errors.As(err, &disabled) {
		t.Errorf("the removed built-in opened: %v", err)
	}
	if after := requests(); after != before {
		t.Errorf("%d requests reached the removed endpoint, want %d", after-before, 0)
	}
}
