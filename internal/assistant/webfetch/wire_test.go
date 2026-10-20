package webfetch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/webfetch"
)

type rawConn struct{ r *bytes.Reader }

func (c *rawConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *rawConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *rawConn) Close() error                     { return nil }
func (c *rawConn) LocalAddr() net.Addr              { return nil }
func (c *rawConn) RemoteAddr() net.Addr             { return nil }
func (c *rawConn) SetDeadline(time.Time) error      { return nil }
func (c *rawConn) SetReadDeadline(time.Time) error  { return nil }
func (c *rawConn) SetWriteDeadline(time.Time) error { return nil }

func TestWireMatchesPython(t *testing.T) {
	f, err := os.Open("testdata/wire.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var c struct {
			Raw string `json:"raw_b64"`
			Ok  *struct {
				Body    string `json:"body_b64"`
				Charset string `json:"charset"`
				URL     string `json:"url"`
			} `json:"ok"`
			Error *corpusError `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(c.Raw)
		fetcher := &webfetch.Fetcher{
			Lookup:  func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{public}, nil },
			Connect: func(context.Context, webfetch.Dest) (net.Conn, error) { return &rawConn{bytes.NewReader(raw)}, nil },
		}
		page, err := fetcher.FetchPage(t.Context(), "http://example.test/x")
		switch {
		case c.Ok != nil && err != nil:
			t.Errorf("%q: %v, want a page", raw, err)
		case c.Ok != nil:
			if base64.StdEncoding.EncodeToString(page.Body) != c.Ok.Body || page.Charset != c.Ok.Charset || page.URL != c.Ok.URL {
				t.Errorf("%q: %q %q %q, want %s %q %q", raw, page.Body, page.Charset, page.URL, c.Ok.Body, c.Ok.Charset, c.Ok.URL)
			}
		case err == nil:
			t.Errorf("%q: a page, want %s: %s", raw, c.Error.Type, c.Error.Message)
		default:
			if class, message := errorType(err); class != c.Error.Type || message != c.Error.Message {
				t.Errorf("%q: %s: %s, want %s: %s", raw, class, message, c.Error.Type, c.Error.Message)
			}
		}
		n++
	}
	if n < 150 {
		t.Errorf("%d wire cases", n)
	}
}
