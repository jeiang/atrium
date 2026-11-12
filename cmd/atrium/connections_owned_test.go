//go:build !modernc

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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

func TestImportedConnectionsStayOperatorOwnedAcrossBoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	host := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(host, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	unsetEnv(t, "ATRIUM_MAIL_MAIL_EMBED_URL")

	notion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ntn-imported-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","results":[{"object":"page","id":"p1"}],"has_more":false}`))
	}))
	t.Cleanup(notion.Close)
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tmdb-imported-token" || r.URL.Path != "/3/configuration" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(tmdb.Close)
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	t.Cleanup(embed.Close)

	source := copyV2Fixture(t, dir)
	editImportSource(t, source, fmt.Sprintf(`INSERT INTO settings (key, value) VALUES
		('media_tmdb_url', %[1]q),
		('media_tmdb_images_url', %[2]q),
		('mail_embed_url', %[3]q),
		('mail_embed_model', 'embed')`, tmdb.URL+"/3", tmdb.URL+"/images", embed.URL+"/v1"))
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('media_tmdb_token', 'tmdb-imported-token', 1762000800, 'fixture')")
	editImportSource(t, source, "INSERT INTO secrets (name, value, set_at, set_by) VALUES ('notion_token', 'ntn-imported-token', 1762000800, 'fixture')")

	if got := runApp(t, "db", "import-v2", source); got.code != cli.ExitOK {
		t.Fatalf("db import-v2: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}
	seedAllFlags(t)

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.ExecContext(context.Background(),
		"UPDATE connections SET config_json = ? WHERE id = 'service/notion'",
		fmt.Sprintf(`{"api":%q}`, notion.URL)); err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	_ = d.Close()

	s := &connectionSite{t: t, dir: dir}
	s.start()
	s.stop()
	s.start()

	for _, id := range []string{"service/notion", "service/tmdb", "service/embedding"} {
		source, enabled, state, _ := connectionRow(t, dir, id)
		if source != "operator" || enabled != 1 || state == "disabled" {
			t.Errorf("%s is %s/%d/%s after two boots", id, source, enabled, state)
		}
	}
	if _, _, _, got := connectionRow(t, dir, "service/tmdb"); !strings.Contains(got, tmdb.URL+"/3") {
		t.Errorf("the imported TMDB endpoint did not survive: %s", got)
	}
	if _, _, _, got := connectionRow(t, dir, "service/notion"); !strings.Contains(got, notion.URL) {
		t.Errorf("the operator Notion endpoint did not survive: %s", got)
	}
	for _, id := range []string{"service/weather", "service/eztv", "service/webfetch"} {
		source, enabled, _, _ := connectionRow(t, dir, id)
		if source != "operator" || enabled != 1 {
			t.Errorf("%s is %s/%d without a host declaration", id, source, enabled)
		}
	}
	listed := runApp(t, "connections", "list")
	for _, id := range []string{"service/notion", "service/tmdb", "service/embedding", "service/weather", "service/eztv"} {
		if !strings.Contains(listed.stdout, id) {
			t.Errorf("connections list lacks %s:\n%s", id, listed.stdout)
		}
	}
	for _, id := range []string{"service/notion", "service/tmdb"} {
		tested := runApp(t, "connections", "test", id)
		if tested.code != cli.ExitOK || !strings.Contains(tested.stdout, "ok") {
			t.Errorf("connections test %s: exit %d stdout %q stderr %q", id, tested.code, tested.stdout, tested.stderr)
		}
	}
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
		return fake, nil
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
	write(`[connections."service/notion"]
kind = "service/notion"
label = "Notion"
config = { api = "http://127.0.0.1:9" }
capabilities = ["api"]
`)
	setEnv(t, "ATRIUM_HOST_CONFIG", host)

	s := &connectionSite{t: t, dir: dir}
	s.start()
	if source, _, _, _ := connectionRow(t, dir, "service/notion"); source != "host" {
		t.Fatalf("the declared connection is %s, not host", source)
	}
	s.stop()
	write("")
	s.start()

	if _, enabled, state, _ := connectionRow(t, dir, "service/notion"); enabled != 0 || state != "disabled" {
		t.Fatalf("the removed connection is enabled=%d state=%s", enabled, state)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	_, err := connections.OpenRead(context.Background(), d.Read, config.Host{}, connections.ID("service/notion"))
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
