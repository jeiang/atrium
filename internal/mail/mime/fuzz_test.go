package mime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func corpusSeeds(f *testing.F) [][]byte {
	f.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "corpora", "features", "*.eml"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seed corpus: %v", err)
	}
	var out [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func noInternal(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "internal error") {
		t.Fatal(err)
	}
}

func walk(t *testing.T, m *Message) {
	for _, k := range m.Keys() {
		_, _, err := m.Get(k)
		noInternal(t, err)
	}
	_, _, err := m.Filename()
	noInternal(t, err)
	_, err = m.IsAttachment()
	noInternal(t, err)
	_, err = m.Body("plain")
	noInternal(t, err)
	_, err = m.Body("html")
	noInternal(t, err)
	_, err = m.Attachments()
	noInternal(t, err)
	if !m.IsMultipart() {
		_, err = m.Content()
		noInternal(t, err)
	}
	for _, p := range m.Parts() {
		walk(t, p)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range corpusSeeds(f) {
		f.Add(s)
	}
	f.Add([]byte("Content-Type: text/plain; name*=\"'\"\r\n\r\nx"))
	f.Fuzz(func(t *testing.T, data []byte) {
		msg, err := Parse(data)
		noInternal(t, err)
		if err == nil {
			walk(t, msg)
		}
	})
}

func FuzzHeaderValue(f *testing.F) {
	for _, name := range []string{"From", "To", "Date", "Subject", "Message-ID", "References", "Content-Type", "Content-Disposition", "Content-Transfer-Encoding", "MIME-Version"} {
		f.Add(name, `"a b" <x@y>, =?utf-8?q?J=C3=B6rg?= (c) <a@[1.2]>; name*=utf-8''r%C3%A9`)
	}
	f.Add("Content-Type", `text/plain; name*="utf-8'en'"`)
	f.Fuzz(func(t *testing.T, name, value string) {
		_, err := headerValue([]rune(name), escapeBytes([]byte(value)), 0, false)
		noInternal(t, err)
	})
}

func TestRFC2231QuotedRaises(t *testing.T) {
	for _, v := range []string{`name*="'"`, `name*="utf-8'en'"`, `name*="a''"`} {
		if _, err := Parse([]byte("Content-Type: text/plain; " + v + "\r\n\r\nx")); err == nil || strings.Contains(err.Error(), "internal error") {
			t.Errorf("Content-Type %s: err = %v, want the header error Python raises", v, err)
		}
		msg, err := Parse([]byte("Content-Disposition: attachment; file" + v + "\r\n\r\nx"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := msg.Filename(); err == nil || strings.Contains(err.Error(), "internal error") {
			t.Errorf("Content-Disposition file%s: err = %v, want the header error Python raises", v, err)
		}
	}
}
