package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func hxFetch(t *testing.T, client *http.Client, method, target string, form url.Values) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range client.Jar.Cookies(req.URL) {
			if c.Name == "atrium_csrf" {
				req.Header.Set("X-CSRF-Token", c.Value)
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text)
}

func cardClient(t *testing.T, base, thread, slot string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, page, _ := fetch(t, client, http.MethodGet, base+"/chat/"+thread, nil)
	if code != http.StatusOK || !strings.Contains(page, `hx-get="`+slot+`"`) {
		t.Fatalf("thread %s has no card slot %s: %d\n%s", thread, slot, code, page)
	}
	return client
}

func TestDraftSentFromTheThreadGoesThroughTheSendGate(t *testing.T) {
	fake := &gmailTokenFake{}
	tokenSrv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", tokenSrv.URL)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
	setEnv(t, "GOOGLE_GMAIL_REFRESH_TOKEN", "refresh")
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

	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO settings (key, value) VALUES ('google_client_id', 'client-id')",
		"INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) VALUES ('oauth', 'OAuth', 'gmail', 'atrium.test@gmail.test', 1, 0, 'xoauth2', 0)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("d1", "draft_email_create", map[string]any{
		"account": "oauth", "to": []any{"to@x.test"}, "subject": "Lease renewal", "body": "Happy to renew.",
	})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("The draft is ready.")))
	if ran := runApp(t, "agent", "run", "--text", "draft the lease reply"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	var draftID string
	if err := d.Read.QueryRow("SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lease renewal'").Scan(&draftID); err != nil {
		t.Fatal(err)
	}

	base := startServeIn(t, dir)
	client := cardClient(t, base, strconv.FormatInt(latestThread(t, dir), 10), "/drafts/"+draftID+"/card")
	code, card := hxFetch(t, client, http.MethodGet, base+"/drafts/"+draftID+"/card", nil)
	if code != http.StatusOK || !strings.Contains(card, `data-card="draft" data-status="draft"`) || !strings.Contains(card, "Sending…") {
		t.Fatalf("draft card: %d\n%s", code, card)
	}
	m := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(card)
	if m == nil {
		t.Fatal("the draft card has no revision")
	}

	code, card = hxFetch(t, client, http.MethodPost, base+"/drafts/"+draftID+"/send", url.Values{"revision": {"999"}})
	if code != http.StatusConflict || !strings.Contains(card, `data-status="draft"`) || !strings.Contains(card, `role="alert"`) || !strings.Contains(card, "Not sent") {
		t.Fatalf("stale send: %d\n%s", code, card)
	}
	if _, data := smtp.seen(); data != "" {
		t.Fatalf("a refused send reached SMTP:\n%s", data)
	}

	code, card = hxFetch(t, client, http.MethodPost, base+"/drafts/"+draftID+"/send", url.Values{"revision": {m[1]}})
	if code != http.StatusOK || !strings.Contains(card, `data-status="sent"`) {
		t.Fatalf("send: %d\n%s", code, card)
	}
	if _, data := smtp.seen(); !strings.Contains(data, "Subject: Lease renewal") {
		t.Fatalf("SMTP did not get the draft:\n%s", data)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM drafts WHERE id = ?", draftID).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("draft status = %q, %v", status, err)
	}
}

func TestProposalApprovedFromTheThreadResolves(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "task_propose", map[string]any{"title": "Renew the passport"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("I proposed a task.")))
	if ran := runApp(t, "agent", "run", "--kind", "cron", "--text", "review"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	var proposalID string
	if err := d.Read.QueryRow("SELECT CAST(id AS TEXT) FROM proposals WHERE title = 'Renew the passport'").Scan(&proposalID); err != nil {
		t.Fatal(err)
	}

	base := startServeIn(t, dir)
	client := cardClient(t, base, strconv.FormatInt(latestThread(t, dir), 10), "/proposals/"+proposalID+"/card")
	code, card := hxFetch(t, client, http.MethodGet, base+"/proposals/"+proposalID+"/card", nil)
	if code != http.StatusOK || !strings.Contains(card, `data-card="proposal" data-status="pending"`) || !strings.Contains(card, "data-card-approve") {
		t.Fatalf("proposal card: %d\n%s", code, card)
	}
	code, card = hxFetch(t, client, http.MethodPost, base+"/proposals/"+proposalID+"/approve", url.Values{"all": {"1"}})
	if code != http.StatusOK || !strings.Contains(card, `data-status="applied"`) {
		t.Fatalf("approve: %d\n%s", code, card)
	}
	var status string
	if err := d.Read.QueryRow("SELECT status FROM proposals WHERE id = ?", proposalID).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("proposal status = %q, %v", status, err)
	}
	var tasks int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM tasks WHERE title = 'Renew the passport'").Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("tasks = %d, %v", tasks, err)
	}
}
