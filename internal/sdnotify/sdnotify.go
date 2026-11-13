package sdnotify

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"
)

const writeTimeout = time.Second

type Notifier struct {
	addr     string
	interval time.Duration
}

func New(getenv func(string) string) *Notifier {
	n := &Notifier{addr: getenv("NOTIFY_SOCKET")}
	if usec, err := strconv.ParseInt(getenv("WATCHDOG_USEC"), 10, 64); err == nil && usec > 0 {
		pid := getenv("WATCHDOG_PID")
		if pid == "" || pid == strconv.Itoa(os.Getpid()) {
			n.interval = time.Duration(usec) * time.Microsecond / 2
		}
	}
	return n
}

func (n *Notifier) Enabled() bool { return n != nil && n.addr != "" }

func (n *Notifier) send(msg string) {
	if !n.Enabled() {
		return
	}
	addr := n.addr
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, _ = conn.Write([]byte(msg))
}

func (n *Notifier) Ready()          { n.send("READY=1") }
func (n *Notifier) Stopping()       { n.send("STOPPING=1") }
func (n *Notifier) Status(s string) { n.send("STATUS=" + s) }
func (n *Notifier) Watchdog()       { n.send("WATCHDOG=1") }

func (n *Notifier) RunWatchdog(ctx context.Context, alive func() bool) {
	if !n.Enabled() || n.interval <= 0 {
		return
	}
	t := time.NewTicker(n.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if alive == nil || alive() {
				n.Watchdog()
			}
		}
	}
}

func (n *Notifier) Watch(ctx context.Context, alive func() bool) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.RunWatchdog(ctx, alive)
	}()
	return func() {
		cancel()
		<-done
	}
}
