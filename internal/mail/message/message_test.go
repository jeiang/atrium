package message_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/message"
)

const rawAlt = "From: Ann <ann@x.test>\r\nTo: me@y.test\r\nSubject: Hi\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n" +
	"Message-ID: <1@x.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: multipart/alternative; boundary=A\r\n\r\n" +
	"--A\r\nContent-Type: text/plain\r\n\r\nplain text\r\n--A\r\nContent-Type: text/html\r\n\r\n<b>html</b>\r\n--A--\r\n" +
	"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--B--\r\n"

const rawHTML = "From: a@x.test\r\nSubject: H\r\nContent-Type: text/html\r\n\r\n<style>p{}</style><p>only &amp; html</p>\r\n"

type fixture struct {
	t     *testing.T
	d     *db.DB
	cache string
	next  int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, d: dbtest.Schema(t), cache: filepath.Join(t.TempDir(), "cache")}
	f.exec(`INSERT INTO accounts (name, kind) VALUES ('icloud', 'icloud')`)
	f.exec(`INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'Bills', 'Bills', 'filed')`)
	return f
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.d.Write.Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.cache, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) add(addr, subject, raw, body string) int64 {
	f.t.Helper()
	f.next++
	var rawPath any
	if raw != "" {
		rawPath = raw
	}
	res, err := f.d.Write.Exec(`INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, raw_path, from_addr, subject, folder, location, first_seen)
		VALUES ('icloud', 1, 1, ?, ?, ?, ?, ?, 'Bills', 'Bills', 0)`, f.next, "d"+string(rune('a'+f.next)), rawPath, addr, subject)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)`, id, subject, addr, body)
	return id
}

func (f *fixture) get(id int64) *message.View {
	f.t.Helper()
	v, err := message.Get(context.Background(), f.d.Read, f.cache, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func TestGetMessageRawParsing(t *testing.T) {
	f := newFixture(t)
	f.write("raw/a.eml", rawAlt)
	f.write("raw/b.eml", rawHTML)
	a := f.add("ann@x.test", "Hi", "raw/a.eml", "indexed")
	b := f.add("a@x.test", "H", "raw/b.eml", "")
	c := f.add("c@x.test", "none", "raw/missing.eml", "indexed body")
	e := f.add("e@x.test", "esc", "../../etc/passwd", "safe")
	f.exec(`INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) VALUES (?, 'icloud', 1, 'operator', 'k', 'file', 'Bills', 0.9, '{"n": 3}')`, a)

	m := f.get(a)
	if m.Body != "plain text" || m.BodySource != "raw" {
		t.Errorf("body = %q from %s", m.Body, m.BodySource)
	}
	if want := []message.Attachment{{Filename: "a.pdf", ContentType: "application/pdf", Size: 5}}; !reflect.DeepEqual(m.Attachments, want) {
		t.Errorf("attachments = %+v", m.Attachments)
	}
	if m.Headers["From"] != "Ann <ann@x.test>" || m.Headers["Message-ID"] != "<1@x.test>" || m.Headers["Cc"] != "" {
		t.Errorf("headers = %v", m.Headers)
	}
	if m.Decision["stage"] != "operator" || m.Folder != "Bills" || m.Decision["detail"].(map[string]any)["n"] != json.Number("3") {
		t.Errorf("decision = %v folder = %q", m.Decision, m.Folder)
	}
	if got := f.get(b); got.Body != "only & html" || got.BodySource != "raw" {
		t.Errorf("html message = %q from %s", got.Body, got.BodySource)
	}
	if missing := f.get(c); missing.Body != "indexed body" || missing.BodySource != "index" || len(missing.Attachments) != 0 {
		t.Errorf("missing raw file = %+v", missing)
	}
	if esc := f.get(e); esc.Body != "safe" || esc.BodySource != "index" {
		t.Errorf("traversal = %q from %s", esc.Body, esc.BodySource)
	}
	if f.get(999) != nil {
		t.Error("unknown message returned a view")
	}
	f.exec(`INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Taxes', 1, 'operator')`, a)
	again := f.get(a)
	if again.CorrectedTo == nil || *again.CorrectedTo != "Taxes" || again.Folder != "Taxes" || again.Decision["stage"] != "operator" {
		t.Errorf("corrected = %+v", again)
	}
}

func TestRawFileRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(cache, "raw"), outside} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(cache, "raw", "a.eml"), filepath.Join(outside, "secret.eml"), filepath.Join(root, "cache-sibling.eml")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(cache, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cache, "raw", "a.eml"), filepath.Join(cache, "inner.eml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"raw/a.eml", true},
		{"raw/../raw/a.eml", true},
		{"inner.eml", true},
		{"", false},
		{"raw", false},
		{"raw/missing.eml", false},
		{"../../etc/passwd", false},
		{"../outside/secret.eml", false},
		{"../cache-sibling.eml", false},
		{"link/secret.eml", false},
		{filepath.Join(outside, "secret.eml"), false},
		{filepath.Join(cache, "raw", "a.eml"), true},
	} {
		got, ok := message.RawFile(cache, tc.raw)
		if ok != tc.ok {
			t.Errorf("RawFile(%q) = %q, %v; want ok = %v", tc.raw, got, ok, tc.ok)
		}
	}
}
