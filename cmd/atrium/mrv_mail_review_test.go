package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/testutil/embedstub"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

type mrvMail struct {
	dir  string
	app  *cli.App
	fake *mailtest.FakeClient
}

const mrvLogin = `S: {icloud.greeting}
C: {t} LOGIN "atrium.test@icloud.com" "app-pass"
S: {t} OK [CAPABILITY {icloud.authcaps}] User logged in
`

const mrvList = `C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: * LIST () "/" "INBOX"
S: * LIST () "/" "Bills"
S: * LIST (\Trash) "/" "Deleted Messages"
S: * LIST (\Sent) "/" "Sent Messages"
S: {t} OK LIST completed
`

const mrvClose = `C: {t} LOGOUT
S: * BYE
S: {t} OK
! close
`

const mrvSentCopyScript = mrvLogin + mrvList + `C/ ^{t} APPEND "Sent Messages" \(\\Seen\) .*\{[0-9]+\+?\}$
C/ ^[\s\S]*Subject: Lunch plans[\s\S]*$
S: {t} OK [APPENDUID 5 1] APPEND completed
` + mrvClose

var mrvCorrectionScript = mrvLogin + `C/ ^{t} STATUS INBOX \(.*\)$
S: * STATUS "INBOX" (MESSAGES 1 UIDVALIDITY 1 UIDNEXT 2 HIGHESTMODSEQ 1)
S: {t} OK STATUS completed
C: {t} EXAMINE INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1] UIDs valid
S: * OK [UIDNEXT 2] Predicted next UID
S: * OK [HIGHESTMODSEQ 1] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 1
S: * SEARCH 1
S: {t} OK SEARCH completed
C/ ^{t} UID FETCH 1 \(.*\)$
S: * 1 FETCH (UID 1 FLAGS (\Seen) BODY[HEADER] {32}
S: Message-ID: <statement-1@x.test>)
S: {t} OK FETCH completed
` + mrvList + mrvList + `C: {t} SELECT INBOX (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 1] UIDs valid
S: * OK [UIDNEXT 2] Predicted next UID
S: * OK [HIGHESTMODSEQ 1] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 1 "Bills"
S: {t} OK [COPYUID 3 1 1] COPY completed
C: {t} UID STORE 1 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 1
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
` + mrvClose + `! accept
` + mrvLogin + mrvList + mrvEmptyBox(`"Bills"`, 3, 1, 1) + mrvEmptyBox(`"Sent Messages"`, 5, 1, 1) + mrvEmptyBox(`INBOX`, 1, 2, 2) + mrvClose + `! accept
` + mrvLogin + `C/ ^{t} STATUS "Bills" \(.*\)$
S: * STATUS "Bills" (MESSAGES 1 UIDVALIDITY 3 UIDNEXT 2 HIGHESTMODSEQ 2)
S: {t} OK STATUS completed
C: {t} EXAMINE "Bills" (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 3] UIDs valid
S: * OK [UIDNEXT 2] Predicted next UID
S: * OK [HIGHESTMODSEQ 2] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C: {t} UID SEARCH UID 1
S: * SEARCH 1
S: {t} OK SEARCH completed
C: {t} SELECT "Bills" (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * OK [PERMANENTFLAGS (\Answered \Flagged \Draft \Deleted \Seen \*)] Limited
S: * 1 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY 3] UIDs valid
S: * OK [UIDNEXT 2] Predicted next UID
S: * OK [HIGHESTMODSEQ 2] Highest
S: {t} OK [READ-WRITE] SELECT completed
C: {t} UID COPY 1 INBOX
S: {t} OK [COPYUID 1 1 2] COPY completed
C: {t} UID STORE 1 +FLAGS.SILENT (\Deleted)
S: {t} OK STORE completed
C: {t} UID EXPUNGE 1
S: * 1 EXPUNGE
S: {t} OK EXPUNGE completed
` + mrvClose + `! accept
` + mrvLogin + mrvList + mrvEmptyBox(`"Bills"`, 3, 2, 3) + mrvEmptyBox(`"Sent Messages"`, 5, 1, 1) + mrvEmptyBox(`INBOX`, 1, 3, 3) + mrvClose

func mrvEmptyBox(name string, validity, next, modseq int) string {
	v, n, s := strconv.Itoa(validity), strconv.Itoa(next), strconv.Itoa(modseq)
	return `C/ ^{t} STATUS ` + regexp.QuoteMeta(name) + ` \(.*\)$
S: * STATUS ` + name + ` (MESSAGES 0 UIDVALIDITY ` + v + ` UIDNEXT ` + n + ` HIGHESTMODSEQ ` + s + `)
S: {t} OK STATUS completed
C: {t} EXAMINE ` + name + ` (CONDSTORE)
S: * FLAGS (\Answered \Flagged \Draft \Deleted \Seen)
S: * 0 EXISTS
S: * 0 RECENT
S: * OK [UIDVALIDITY ` + v + `] UIDs valid
S: * OK [UIDNEXT ` + n + `] Predicted next UID
S: * OK [HIGHESTMODSEQ ` + s + `] Highest
S: {t} OK [READ-ONLY] EXAMINE completed
C/ ^{t} UID SEARCH .*$
S: * SEARCH
S: {t} OK SEARCH completed
`
}

func mrvMailFixture(t *testing.T, mode string) *mrvMail {
	t.Helper()
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	m := &mrvMail{dir: dir, fake: mailtest.NewFakeClient()}
	inbox := m.fake.AddBox("INBOX", mailtest.NewBox(1))
	m.fake.AddBox("Bills", mailtest.NewBox(3))
	m.fake.AddBox("Deleted Messages", mailtest.NewBox(9), `\Trash`)
	m.fake.AddBox("Sent Messages", mailtest.NewBox(5), `\Sent`)
	h, x := mailtest.RawWith("Statement", "pay@bank.test", "statement-1", "Your statement is ready.")
	inbox.Add(h, x, []string{`\Seen`}, nil)
	f.app = newAppWith(f.lookup, func(config.Config) mail.OpenFunc {
		return func(ctx context.Context, opts mail.OpenOptions) (mail.Client, error) {
			return m.fake.Open(ctx, opts)
		}
	}, nil, quietLog)
	f.settings, f.state = f.app.Settings, f.app.State
	m.app = f.app
	embed := embedstub.Server(t)
	seedSettings(t, map[string]string{mail.KeyMode: mode})
	setEnv(t, "ATRIUM_HOST_CONFIG", accountsFileEmbedding(t, dir, embed.URL+"/v1", filepath.Join(testRoot, "host", "anthropic.key"), mailAccount{"icloud", "icloud", "atrium.test@icloud.com", "app-pass", false}))
	return m
}

func mrvClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func mrvDo(t *testing.T, client *http.Client, method, target string, form url.Values, headers ...string) (int, string, http.Header) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if client.Jar != nil {
			for _, c := range client.Jar.Cookies(req.URL) {
				if c.Name == "atrium_csrf" {
					req.Header.Set("X-CSRF-Token", c.Value)
				}
			}
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text), resp.Header
}

func mrvScalar(t *testing.T, path, query string, args ...any) string {
	t.Helper()
	d := mustConnect(t, path, db.Key{})
	defer func() { _ = d.Close() }()
	var out *string
	if err := d.Read.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if out == nil {
		return "<nil>"
	}
	return *out
}

func TestMrvMailCorrectionHonorsShadowPauseAndUndoesLive(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	if got := runApp(t, "sync"); got.code != cli.ExitOK {
		t.Fatalf("sync: %d %s", got.code, got.stderr)
	}
	dbPath := filepath.Join(m.dir, "atrium.db")
	id := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM messages WHERE subject = 'Statement'")
	correct := "/messages/" + id + "/correct"
	hx := []string{"HX-Request", "true"}

	t.Setenv("ATRIUM_IMAP_ADDR", "127.0.0.1:1")
	base, _, stop := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	mrvDo(t, client, http.MethodGet, base+"/triage", nil)
	if code, _, _ := mrvDo(t, http.DefaultClient, http.MethodPost, base+correct, url.Values{"dest": {"Bills"}}); code != http.StatusForbidden {
		t.Fatalf("a correction without the CSRF token = %d, want 403", code)
	}
	code, body, _ := mrvDo(t, client, http.MethodPost, base+correct, url.Values{"dest": {"INBOX"}, "context": {"message"}}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "invalid destination") {
		t.Fatalf("a correction to INBOX = %d: %.400s", code, body)
	}
	if n := mrvScalar(t, dbPath, "SELECT CAST(COUNT(*) AS TEXT) FROM corrections"); n != "0" {
		t.Fatalf("refused corrections were stored: %s", n)
	}
	code, body, _ = mrvDo(t, client, http.MethodPost, base+correct, url.Values{"dest": {"Bills"}, "context": {"row"}}, hx...)
	if code != http.StatusOK {
		t.Fatalf("shadow correction = %d: %.400s", code, body)
	}
	if got := mrvScalar(t, dbPath, "SELECT folder FROM corrections WHERE message_id = ?", id); got != "Bills" {
		t.Fatalf("shadow correction folder = %s", got)
	}
	stop()

	seedSettings(t, map[string]string{mail.KeyMode: "live"})
	for _, args := range [][]string{{"live", "rebaseline"}, {"live", "pause"}} {
		if got := runApp(t, args...); got.code != cli.ExitOK {
			t.Fatalf("%v: %d %s", args, got.code, got.stderr)
		}
	}
	base, _, _ = startServeApp(t, m.app, &lockedBuffer{})
	client = mrvClient(t)
	mrvDo(t, client, http.MethodGet, base+"/triage", nil)
	code, body, _ = mrvDo(t, client, http.MethodPost, base+correct, url.Values{"dest": {"Bills"}, "context": {"row"}}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "Moving is paused; it will move after you resume.") || strings.Contains(body, "could not") {
		t.Fatalf("paused correction = %d: %.400s", code, body)
	}
	host := strings.TrimPrefix(base, "http://")
	code, _, header := mrvDo(t, client, http.MethodPost, base+"/live/resume", url.Values{}, "Referer", "http://"+host+"//evil.test")
	if code != http.StatusSeeOther || header.Get("Location") != "/triage" {
		t.Fatalf("resume with a foreign referer = %d %q, want a redirect to /triage", code, header.Get("Location"))
	}
	if got := runApp(t, "live", "status"); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"paused": false`) {
		t.Fatalf("live status after resume: %d %s", got.code, got.stdout)
	}

	server, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, mrvCorrectionScript))
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)
	code, body, _ = mrvDo(t, client, http.MethodPost, base+correct, url.Values{"dest": {"Bills"}, "context": {"row"}}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "The message was moved.") {
		t.Fatalf("live correction = %d: %.400s", code, body)
	}
	if state := mrvScalar(t, dbPath, "SELECT state FROM effects WHERE origin = 'mail_auto' AND operation = 'correct'"); state != "done" {
		t.Fatalf("correction action state = %s", state)
	}
	action := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM effects WHERE origin = 'mail_auto' AND operation = 'correct' AND state = 'done'")
	if got := mrvScalar(t, dbPath, "SELECT outcome || ' ' || details_json FROM operation_audit WHERE operation = 'mail.correct' ORDER BY id DESC LIMIT 1"); got != `ok {"effect_ids":[`+action+`]}` {
		t.Fatalf("the live correction audit = %s, want ok with effect %s", got, action)
	}
	code, body, _ = mrvDo(t, client, http.MethodPost, base+"/live/actions/"+action+"/undo", url.Values{}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "Undone") {
		t.Fatalf("undo = %d: %.400s", code, body)
	}
	code, body, _ = mrvDo(t, client, http.MethodPost, base+"/live/actions/"+action+"/undo", url.Values{}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "Not undone") {
		t.Fatalf("second undo = %d: %.400s", code, body)
	}
	if code, _, _ := mrvDo(t, client, http.MethodPost, base+"/live/actions/999999/undo", url.Values{}, hx...); code != http.StatusNotFound {
		t.Fatalf("undo of an unknown action = %d, want 404", code)
	}
}

type mrvSMTP struct {
	mu    sync.Mutex
	mode  string
	datas []string
}

func mrvSMTPServer(t *testing.T) *mrvSMTP {
	t.Helper()
	ca := smtpCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &mrvSMTP{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	t.Setenv("ATRIUM_SMTP_ADDR", ln.Addr().String())
	t.Setenv("ATRIUM_SMTP_CA", ca)
	return f
}

func (f *mrvSMTP) set(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
}

func (f *mrvSMTP) current() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

func (f *mrvSMTP) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.datas...)
}

func (f *mrvSMTP) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	write := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")
	tlsOn := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		upper := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-fake")
			if !tlsOn {
				write("250-STARTTLS")
			}
			write("250-AUTH PLAIN")
			write("250 OK")
		case upper == "STARTTLS":
			write("220 ready")
			tc := tls.Server(c, smtpTLSConfig)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
		case strings.HasPrefix(upper, "AUTH "):
			if f.current() == "auth-fail" {
				write("535 5.7.8 authentication failed")
			} else {
				write("235 2.7.0 accepted")
			}
		case upper == "DATA":
			write("354 go ahead")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			f.mu.Lock()
			f.datas = append(f.datas, body.String())
			drop := f.mode == "drop"
			f.mu.Unlock()
			if drop {
				return
			}
			write("250 queued")
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func TestMrvDraftSendNeverResendsAnUncertainMailAndFilesTheSentCopy(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	smtp := mrvSMTPServer(t)
	toolResult(t, "draft_email_create", map[string]any{"account": "icloud", "to": []any{"friend@x.test"}, "subject": "Lunch plans", "body": "Noon works."})
	dbPath := filepath.Join(m.dir, "atrium.db")
	id := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lunch plans'")
	draft := "/drafts/" + id
	status := func() string { return mrvScalar(t, dbPath, "SELECT status FROM drafts WHERE id = ?", id) }
	hx := []string{"HX-Request", "true"}

	t.Setenv("ATRIUM_IMAP_ADDR", "127.0.0.1:1")
	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	revision := func() string {
		t.Helper()
		code, page, _ := mrvDo(t, client, http.MethodGet, base+draft, nil)
		found := regexp.MustCompile(`name="revision" value="([0-9]+)"`).FindStringSubmatch(page)
		if code != http.StatusOK || found == nil {
			t.Fatalf("draft page = %d without a revision: %.400s", code, page)
		}
		return found[1]
	}

	smtp.set("auth-fail")
	code, body, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {revision()}}, hx...)
	if code != http.StatusConflict || !strings.Contains(body, "Not sent") || !strings.Contains(body, "535") {
		t.Fatalf("send with a refused login = %d: %.400s", code, body)
	}
	if got := status(); got != "draft" || len(smtp.sent()) != 0 {
		t.Fatalf("after a refused login the draft is %q and %d messages went out", got, len(smtp.sent()))
	}
	smtp.set("drop")
	stale := revision()
	code, body, _ = mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {stale}}, hx...)
	if code != http.StatusConflict || !strings.Contains(body, "Send outcome unknown") || strings.Contains(body, "Not sent") {
		t.Fatalf("send lost after DATA = %d: %.400s", code, body)
	}
	if got := status(); got != "unknown" || len(smtp.sent()) != 1 {
		t.Fatalf("after a lost reply the draft is %q with %d deliveries", got, len(smtp.sent()))
	}
	if outcome := mrvScalar(t, dbPath, "SELECT outcome FROM operation_audit WHERE operation = 'mail.draft-send' ORDER BY id DESC LIMIT 1"); outcome != "unknown" {
		t.Fatalf("uncertain send audit = %s, want unknown", outcome)
	}
	unknownEffect := mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM effects WHERE operation = 'smtp_send' AND state = 'unknown'")
	if effect := mrvScalar(t, dbPath, "SELECT CAST(json_extract(details_json, '$.effect_ids[0]') AS TEXT) FROM operation_audit WHERE operation = 'mail.draft-send' ORDER BY id DESC LIMIT 1"); effect != unknownEffect {
		t.Fatalf("uncertain send audit effect = %s, want %s", effect, unknownEffect)
	}
	smtp.set("")
	for _, rev := range []string{stale, strconv.Itoa(mrvAtoi(t, stale) + 1)} {
		if code, _, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {rev}}, hx...); code != http.StatusConflict {
			t.Fatalf("resend of an uncertain draft at revision %s = %d, want 409", rev, code)
		}
	}
	if n := len(smtp.sent()); n != 1 {
		t.Fatalf("an uncertain draft was sent again: %d deliveries", n)
	}

	if code, body, _ := mrvDo(t, client, http.MethodPost, base+draft+"/resolve", url.Values{"outcome": {"draft"}}, hx...); code != http.StatusOK || status() != "draft" {
		t.Fatalf("back to draft = %d, status %q: %.400s", code, status(), body)
	}
	if effect := mrvScalar(t, dbPath, "SELECT CAST(json_extract(details_json, '$.effect_ids[0]') AS TEXT) FROM operation_audit WHERE operation = 'mail.draft-resolve' ORDER BY id DESC LIMIT 1"); effect != unknownEffect {
		t.Fatalf("resolved draft audit effect = %s, want %s", effect, unknownEffect)
	}
	code, body, _ = mrvDo(t, client, http.MethodPost, base+draft+"/resolve", url.Values{"outcome": {"sent"}}, hx...)
	if code != http.StatusConflict || !strings.Contains(body, "not in the unknown state") {
		t.Fatalf("resolving a settled draft = %d: %.400s", code, body)
	}

	final := revision()
	code, _, _ = mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {strconv.Itoa(mrvAtoi(t, final) - 1)}}, hx...)
	if code != http.StatusConflict || len(smtp.sent()) != 1 {
		t.Fatalf("stale send = %d with %d deliveries, want 409 and one previous delivery", code, len(smtp.sent()))
	}
	if outcome := mrvScalar(t, dbPath, "SELECT outcome FROM operation_audit WHERE operation = 'mail.draft-send' ORDER BY id DESC LIMIT 1"); outcome != "conflict" {
		t.Fatalf("stale send audit = %s, want conflict", outcome)
	}
	code, body, _ = mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {final}}, hx...)
	if code != http.StatusOK || !strings.Contains(body, "copy filed in Sent") {
		t.Fatalf("send = %d: %.400s", code, body)
	}
	if got := smtp.sent(); status() != "sent" || len(got) != 2 || !strings.Contains(got[1], "Subject: Lunch plans") {
		t.Fatalf("after send the draft is %q with deliveries %q", status(), got)
	}
	for _, rev := range []string{final, strconv.Itoa(mrvAtoi(t, final) + 1)} {
		if code, _, _ := mrvDo(t, client, http.MethodPost, base+draft+"/send", url.Values{"revision": {rev}}, hx...); code != http.StatusConflict || len(smtp.sent()) != 2 {
			t.Fatalf("a sent draft sent again at revision %s: %d, %d deliveries", rev, code, len(smtp.sent()))
		}
	}
}

func mrvAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMrvUndoingAssistantWritesRestoresAndRefusesWhenChangedSince(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w1", "file_write", map[string]any{"name": "note.txt", "content": "first"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("w2", "file_write", map[string]any{"name": "note.txt", "content": "second"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p1", "kb_write", map[string]any{"title": "tz", "body": "AST", "scope": "personal"})))
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p2", "kb_write", map[string]any{"id": 1, "body": "UTC"})))
	api.Push(anthropictest.Reply("end_turn", anthropictest.Text("done")))
	if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "write the note and set my timezone"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	dbPath := filepath.Join(dir, "atrium.db")
	writeID := func(tool string, nth int) string {
		return mrvScalar(t, dbPath, "SELECT CAST(id AS TEXT) FROM effects WHERE direction = 'forward' AND operation = ? ORDER BY id LIMIT 1 OFFSET ?", tool, nth)
	}
	fileFirst, fileSecond := writeID("file_write", 0), writeID("file_write", 1)
	profileFirst, profileSecond := writeID("kb_write", 0), writeID("kb_write", 1)
	note := filepath.Join(dir, "files", "1", "note.txt")
	content := func() string {
		data, err := os.ReadFile(note)
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(data)
	}
	profile := func() string {
		d := mustConnect(t, dbPath, db.Key{})
		defer func() { _ = d.Close() }()
		var value string
		var retracted *float64
		if err := d.Read.QueryRow("SELECT body, retracted_at FROM kb_records WHERE id = 1").Scan(&value, &retracted); err != nil {
			return "<none>"
		}
		if retracted != nil {
			return "<retracted>"
		}
		return value
	}
	if content() != "second" || profile() != "UTC" {
		t.Fatalf("after the run note = %q, tz = %q", content(), profile())
	}

	base := startServeIn(t, dir)
	client := mrvClient(t)
	code, page, _ := mrvDo(t, client, http.MethodGet, base+"/activity", nil)
	if code != http.StatusOK || !strings.Contains(page, "/activity/effect/"+fileSecond+"/undo") || !strings.Contains(page, "/activity/effect/"+profileSecond+"/undo") {
		t.Fatalf("activity = %d without the undo controls: %.400s", code, page)
	}
	undo := func(id string) (int, string) {
		t.Helper()
		code, body, _ := mrvDo(t, client, http.MethodPost, base+"/activity/effect/"+id+"/undo", url.Values{})
		return code, body
	}

	if code, body := undo(fileSecond); code != http.StatusSeeOther || content() != "first" {
		t.Fatalf("undo of the overwrite = %d, note %q: %s", code, content(), body)
	}
	if err := os.WriteFile(note, []byte("changed"), 0o666); err != nil {
		t.Fatal(err)
	}
	if code, body := undo(fileFirst); code != http.StatusConflict || !strings.Contains(body, "file changed since the write") || content() != "changed" {
		t.Fatalf("undo over an edited file = %d, note %q: %s", code, content(), body)
	}
	if code, _ := undo(fileSecond); code != http.StatusConflict || content() != "changed" {
		t.Fatalf("a second undo of the overwrite = %d, note %q", code, content())
	}

	if code, body := undo(profileFirst); code != http.StatusConflict || !strings.Contains(body, "changed since this write") || profile() != "UTC" {
		t.Fatalf("undo of a superseded profile write = %d, tz %q: %s", code, profile(), body)
	}
	if code, body := undo(profileSecond); code != http.StatusSeeOther || profile() != "AST" {
		t.Fatalf("undo of the latest record write = %d, body %q: %s", code, profile(), body)
	}
	if code, body := undo(profileFirst); code != http.StatusConflict || !strings.Contains(body, "changed since this write") || profile() != "AST" {
		t.Fatalf("undo of a superseded record write = %d, body %q: %s", code, profile(), body)
	}
}

func TestMrvDecisionsListEachDecisionOnceAcrossCheckAndFiled(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	h, x := mailtest.RawWith("Second statement", "pay@bank.test", "statement-2", "Another statement is ready.")
	m.fake.Boxes["INBOX"].Box.Add(h, x, []string{`\Seen`}, nil)
	if got := runApp(t, "sync"); got.code != cli.ExitOK {
		t.Fatalf("sync: %d %s", got.code, got.stderr)
	}
	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	code, body, _ := mrvDo(t, client, http.MethodGet, base+"/triage", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /triage = %d", code)
	}
	seen := map[string]int{}
	for _, part := range strings.Split(body, `id="decision-`)[1:] {
		seen[part[:strings.IndexByte(part, '"')]]++
	}
	if len(seen) == 0 {
		t.Fatal("the decisions page lists no decision after a sync")
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("decision %s is listed %d times", id, n)
		}
	}
	check, _, _ := strings.Cut(body, ">Filed<")
	if n := strings.Count(check, `id="decision-`); n != 1 || !strings.Contains(check, "1 more from this sender") {
		t.Errorf("two decisions to check from one sender show %d rows, with the count %v", n, strings.Contains(check, "1 more from this sender"))
	}
	if code, all, _ := mrvDo(t, client, http.MethodGet, base+"/triage?q=pay%40bank.test", nil); code != http.StatusOK || strings.Count(all, "tatement</a>") != 2 {
		t.Errorf("the sender's list = %d with %d statements, want 2", code, strings.Count(all, "tatement</a>"))
	}
	if strings.Contains(body, "data-check") != strings.Contains(body, ">Filed<") {
		t.Error("the check section and the Filed heading do not appear together")
	}
	code, filtered, _ := mrvDo(t, client, http.MethodGet, base+"/triage?agreement=pending", nil)
	if code != http.StatusOK || strings.Contains(filtered, "data-check") {
		t.Errorf("a filtered list = %d, shows the check section %v", code, strings.Contains(filtered, "data-check"))
	}
}

func TestMrvDraftSendAuditsDeliveredMailWithAFailedSentCopyAsPartial(t *testing.T) {
	m := mrvMailFixture(t, "shadow")
	smtp := mrvSMTPServer(t)
	m.fake.FailWrite = func(write mailtest.FakeWrite) error {
		if write.Verb == "APPEND" {
			return errors.New("Sent copy refused")
		}
		return nil
	}
	toolResult(t, "draft_email_create", map[string]any{"account": "icloud", "to": []any{"friend@x.test"}, "subject": "Lunch plans", "body": "Noon works."})
	path := filepath.Join(m.dir, "atrium.db")
	id := mrvScalar(t, path, "SELECT CAST(id AS TEXT) FROM drafts WHERE subject = 'Lunch plans'")
	revision := mrvScalar(t, path, "SELECT CAST(revision AS TEXT) FROM drafts WHERE id = ?", id)
	base, _, _ := startServeApp(t, m.app, &lockedBuffer{})
	client := mrvClient(t)
	if code, _, _ := mrvDo(t, client, http.MethodGet, base+"/drafts/"+id, nil); code != http.StatusOK {
		t.Fatalf("draft page = %d", code)
	}
	code, _, _ := mrvDo(t, client, http.MethodPost, base+"/drafts/"+id+"/send", url.Values{"revision": {revision}}, "HX-Request", "true")
	if code != http.StatusOK || len(smtp.sent()) != 1 {
		t.Fatalf("delivered send = %d with %d deliveries", code, len(smtp.sent()))
	}
	if outcome := mrvScalar(t, path, "SELECT outcome FROM operation_audit WHERE operation = 'mail.draft-send' ORDER BY id DESC LIMIT 1"); outcome != "partial" {
		t.Fatalf("failed Sent-copy audit = %s, want partial", outcome)
	}
	if count := mrvScalar(t, path, "SELECT CAST(json_array_length(details_json, '$.effect_ids') AS TEXT) FROM operation_audit WHERE operation = 'mail.draft-send' ORDER BY id DESC LIMIT 1"); count != "2" {
		t.Fatalf("failed Sent-copy audit effect count = %s, want 2", count)
	}
}
