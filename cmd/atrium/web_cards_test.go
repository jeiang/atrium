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

func cardClient(t *testing.T, base, thread string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	code, page, _ := fetch(t, client, http.MethodGet, base+"/chat/"+thread, nil)
	if code != http.StatusOK {
		t.Fatalf("thread %s: %d\n%s", thread, code, page)
	}
	return client
}

func TestDraftSentFromTheThreadGoesThroughTheSendGate(t *testing.T) {
	fake := &gmailTokenFake{}
	tokenSrv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(tokenSrv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", tokenSrv.URL)
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	googleHostConfig(t, dir)
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
	insertMailAccount(t, d, "oauth", "gmail", "atrium.test@gmail.test", "xoauth2")

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
	client := cardClient(t, base, strconv.FormatInt(latestThread(t, dir), 10))
	code, card := hxFetch(t, client, http.MethodGet, base+"/drafts/"+draftID+"/card", nil)
	if code != http.StatusOK {
		t.Fatalf("draft card: %d\n%s", code, card)
	}
	m := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(card)
	if m == nil {
		t.Fatal("the draft card has no revision")
	}

	code, card = hxFetch(t, client, http.MethodPost, base+"/drafts/"+draftID+"/send", url.Values{"revision": {"999"}})
	if code != http.StatusConflict {
		t.Fatalf("stale send: %d\n%s", code, card)
	}
	if _, data := smtp.seen(); data != "" {
		t.Fatalf("a refused send reached SMTP:\n%s", data)
	}

	code, card = hxFetch(t, client, http.MethodPost, base+"/drafts/"+draftID+"/send", url.Values{"revision": {m[1]}})
	if code != http.StatusOK {
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

func TestCalendarCreateAsksInTheThreadAndWritesOnlyOnApproval(t *testing.T) {
	dir, srv := davFixture(t)
	id := proposalID(t, toolResult(t, "calendar_create", map[string]any{
		"calendar": "icloud", "title": "Dentist", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00",
	}))
	if len(srv.Puts) != 0 || len(srv.Deleted) != 0 {
		t.Fatalf("asking wrote to the calendar: puts %v deletes %v", srv.Puts, srv.Deleted)
	}
	thread := strconv.FormatInt(latestThread(t, dir), 10)
	base := startServeIn(t, dir)
	card := base + "/proposals/" + id + "/card"
	client := cardClient(t, base, thread)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	if code, _, header := fetch(t, client, http.MethodGet, base+"/proposals/"+id, nil); code != http.StatusSeeOther || header.Get("Location") != "/chat/"+thread+"#proposal-"+id {
		t.Fatalf("GET /proposals/%s = %d %q, want a redirect to the thread card", id, code, header.Get("Location"))
	}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/proposals", nil); code != http.StatusNotFound {
		t.Fatalf("GET /proposals = %d, want 404", code)
	}
	for _, path := range []string{"/proposals/0", "/proposals/0/card", "/proposals/-1/card"} {
		if code, body := hxFetch(t, client, http.MethodGet, base+path, nil); code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404\n%s", path, code, body)
		}
	}
	if code, body := hxFetch(t, client, http.MethodGet, base+"/review/strip", nil); code != http.StatusOK {
		t.Fatalf("review strip: %d\n%s", code, body)
	}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/chat/"+thread, nil); code != http.StatusOK {
		t.Fatalf("thread with pending approval: %d", code)
	}
	code, body := hxFetch(t, client, http.MethodGet, card, nil)
	if code != http.StatusOK {
		t.Fatalf("pending card: %d\n%s", code, body)
	}
	if code, body = hxFetch(t, client, http.MethodGet, card+"?poll=1", nil); code != http.StatusNoContent {
		t.Fatalf("poll of a pending card = %d, want 204\n%s", code, body)
	}
	if code, body = hxFetch(t, client, http.MethodGet, card+"?view=row", nil); code != http.StatusOK {
		t.Fatalf("Needs you row: %d\n%s", code, body)
	}

	code, body = hxFetch(t, client, http.MethodPost, base+"/proposals/"+id+"/approve", url.Values{"all": {"1"}, "view": {"row"}})
	if code != http.StatusOK {
		t.Fatalf("approve from the Work panel: %d\n%s", code, body)
	}
	if len(srv.Puts) != 1 || len(srv.Deleted) != 0 {
		t.Fatalf("approval wrote %d events and deleted %d, want one PUT", len(srv.Puts), len(srv.Deleted))
	}
	if code, body = hxFetch(t, client, http.MethodGet, card+"?poll=1", nil); code != http.StatusOK {
		t.Fatalf("poll of the applied card: %d\n%s", code, body)
	}
	if code, _, _ := fetch(t, client, http.MethodGet, base+"/chat/"+thread, nil); code != http.StatusOK {
		t.Fatalf("thread after approval: %d", code)
	}
	if code, body = hxFetch(t, client, http.MethodPost, base+"/proposals/"+id+"/approve", url.Values{"all": {"1"}}); len(srv.Puts) != 1 {
		t.Fatalf("a second approval wrote again: %d %d puts\n%s", code, len(srv.Puts), body)
	}
}
