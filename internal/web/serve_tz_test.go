package web

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestServeRejectsInvalidTimezoneBeforeListening(t *testing.T) {
	t.Setenv("ATRIUM_TZ", "Mars/Base")
	port := freePort(t)
	code, _, stderr := runServe(t, context.Background(), "--port", port, "--auth", "local")
	if code != cli.ExitError || !strings.Contains(stderr, "error: ATRIUM_TZ is not a time zone: 'Mars/Base'. Use an IANA name such as America/Port_of_Spain.") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("serve opened a listener with an invalid timezone")
	}
}
