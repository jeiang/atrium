package sdnotifytest

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

type Socket struct {
	t        *testing.T
	mu       sync.Mutex
	msgs     []string
	path     string
	watchdog time.Duration
}

func Listen(t *testing.T, watchdog time.Duration) *Socket {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "n")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &Socket{t: t, path: path, watchdog: watchdog}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, string(buf[:n]))
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *Socket) Env() map[string]string {
	usec := ""
	if s.watchdog > 0 {
		usec = strconv.FormatInt(s.watchdog.Microseconds(), 10)
	}
	return map[string]string{"NOTIFY_SOCKET": s.path, "WATCHDOG_PID": "", "WATCHDOG_USEC": usec}
}

func (s *Socket) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.msgs)
}

func (s *Socket) Wait(want string) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(s.Messages(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("no %q among %q", want, s.Messages())
}
