package webfetch_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/webfetch"
)

func refusedWithoutConnecting(t *testing.T, dns map[string][]string, urls ...string) {
	t.Helper()
	for _, url := range urls {
		s := newScenario(manifest{DNS: dns})
		_, err := s.fetcher().FetchPage(t.Context(), url)
		var blocked webfetch.Blocked
		if !errors.As(err, &blocked) {
			t.Errorf("%s: error %v, want Blocked", url, err)
		}
		if len(s.connections) != 0 {
			t.Errorf("%s: connected %+v", url, s.connections)
		}
	}
}

func TestSSRFChecklist(t *testing.T) {
	dns := map[string][]string{
		"example.test":          {"93.184.216.34"},
		"localhost":             {"127.0.0.1", "::1"},
		"metadata.example.test": {"169.254.169.254"},
		"innocent.example.test": {"10.1.2.3"},
		"mapped.example.test":   {"::ffff:10.0.0.1"},
		"rebind.example.test":   {"93.184.216.34", "127.0.0.1"},
		"cgnat.example.test":    {"100.64.0.1"},
	}
	t.Run("private addresses", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://10.0.0.5/", "http://172.16.0.1/", "http://172.31.255.255/", "http://192.168.1.1/", "http://100.64.0.1/",
			"http://innocent.example.test/", "http://cgnat.example.test/", "http://[fc00::1]/", "http://[fd12:3456::1]/")
	})
	t.Run("loopback", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://127.0.0.1/", "http://127.255.255.254/", "http://[::1]/", "http://localhost/", "http://LOCALHOST./", "http://0.0.0.0/", "http://[::]/")
	})
	t.Run("link-local", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://169.254.1.1/", "http://[fe80::1]/", "http://[fe80::1%25eth0]/")
	})
	t.Run("metadata", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://169.254.169.254/latest/meta-data/", "http://metadata.example.test/", "http://[fd00:ec2::254]/", "http://[::ffff:169.254.169.254]/")
	})
	t.Run("IPv4-mapped and IPv6 literals", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://[::ffff:127.0.0.1]/", "http://[::ffff:7f00:1]/", "http://[::ffff:0:7f00:1]/", "http://[64:ff9b::7f00:1]/",
			"http://[::127.0.0.1]/", "http://[fec0::1]/", "http://[2001:db8::1]/", "http://mapped.example.test/", "http://rebind.example.test/")
	})
	t.Run("numeric host spellings", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://2130706433/", "http://0x7f.1/", "http://0177.0.0.1/", "http://127.1/", "http://017700000001/")
	})
	t.Run("userinfo tricks", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "http://example.test@127.0.0.1/", "http://127.0.0.1#@example.test/", "http://a@b@127.0.0.1/", "http://example.test:80@127.0.0.1/")
		for _, url := range []string{"http://127.0.0.1:80@example.test/", `http://127.0.0.1\@example.test/`} {
			s := newScenario(manifest{DNS: dns, Responses: []scriptedResponse{{Scheme: "http", Host: "example.test", Port: 80, Target: "/", Status: 200}}})
			if _, err := s.fetcher().FetchPage(t.Context(), url); err != nil {
				t.Errorf("%s: %v", url, err)
			}
			if len(s.connections) != 1 || s.connections[0].Host != "example.test" || s.connections[0].Address != "93.184.216.34" {
				t.Errorf("%s: connections %+v, want one to example.test at its public address", url, s.connections)
			}
		}
	})
	t.Run("schemes", func(t *testing.T) {
		refusedWithoutConnecting(t, dns, "file:///etc/passwd", "ftp://example.test/", "gopher://example.test/", "http:///x", "javascript:alert(1)", "data:text/html,<p>x</p>", "//example.test/")
	})
	t.Run("DNS rebinding", func(t *testing.T) {
		m := manifest{
			DNSSequences: map[string][][]string{"flip.example.test": {{"93.184.216.34"}, {"127.0.0.1"}}},
			Responses: []scriptedResponse{
				{Scheme: "http", Host: "flip.example.test", Port: 80, Target: "/ok", Status: 200},
				{Scheme: "http", Host: "flip.example.test", Port: 80, Target: "/hop", Status: 302, Headers: [][2]string{{"Location", "/ok"}}},
			},
		}
		s := newScenario(m)
		if _, err := s.fetcher().FetchPage(t.Context(), "http://flip.example.test/ok"); err != nil {
			t.Fatal(err)
		}
		if len(s.lookups) != 1 || len(s.connections) != 1 || s.connections[0].Address != "93.184.216.34" {
			t.Errorf("one hop: lookups %v, connections %+v", s.lookups, s.connections)
		}
		s = newScenario(m)
		_, err := s.fetcher().FetchPage(t.Context(), "http://flip.example.test/hop")
		var blocked webfetch.Blocked
		if !errors.As(err, &blocked) || len(s.connections) != 1 || len(s.lookups) != 2 {
			t.Errorf("redirect: %v, lookups %v, connections %+v", err, s.lookups, s.connections)
		}
	})
	t.Run("redirect to a private host", func(t *testing.T) {
		for _, target := range []string{"http://192.168.1.1/secret", "http://169.254.169.254/latest", "http://metadata.example.test/latest", "http://[::1]/", "file:///etc/passwd"} {
			m := manifest{DNS: dns, Responses: []scriptedResponse{{Scheme: "http", Host: "example.test", Port: 80, Target: "/hop", Status: 302, Headers: [][2]string{{"Location", target}}}}}
			s := newScenario(m)
			_, err := s.fetcher().FetchPage(t.Context(), "http://example.test/hop")
			var blocked webfetch.Blocked
			if !errors.As(err, &blocked) || len(s.connections) != 1 {
				t.Errorf("%s: %v, connections %+v", target, err, s.connections)
			}
		}
	})
	t.Run("oversize body", func(t *testing.T) {
		var declared, streamed scriptedResponse
		declared = scriptedResponse{Scheme: "http", Host: "example.test", Port: 80, Target: "/declared", Status: 200, Headers: [][2]string{{"Content-Type", "text/plain"}, {"Content-Length", "2097153"}}}
		streamed = scriptedResponse{Scheme: "http", Host: "example.test", Port: 80, Target: "/streamed", Status: 200, Headers: [][2]string{{"Content-Type", "text/plain"}}, Chunk: 65536}
		streamed.Body.RepeatB64, streamed.Body.Count = "YQ==", webfetch.MaxBytes+1
		for _, target := range []string{"/declared", "/streamed"} {
			s := newScenario(manifest{DNS: dns, Responses: []scriptedResponse{declared, streamed}})
			_, err := s.fetcher().FetchPage(t.Context(), "http://example.test"+target)
			var fe webfetch.FetchError
			if !errors.As(err, &fe) || err.Error() != "too large" {
				t.Errorf("%s: %v", target, err)
			}
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f := &webfetch.Fetcher{
			Timeout: 100 * time.Millisecond,
			Lookup:  newScenario(manifest{DNS: dns}).lookup,
			Connect: func(ctx context.Context, d webfetch.Dest) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		_, err := f.FetchPage(t.Context(), "http://example.test/")
		var fe webfetch.FetchError
		if !errors.As(err, &fe) || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error %v", err)
		}
	})
}
