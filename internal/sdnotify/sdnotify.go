package sdnotify

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

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

func FromEnv() *Notifier { return New(os.Getenv) }

func (n *Notifier) Enabled() bool { return n != nil && n.addr != "" }

func (n *Notifier) Interval() time.Duration {
	if n == nil {
		return 0
	}
	return n.interval
}

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
	_, _ = conn.Write([]byte(msg))
}

func (n *Notifier) Ready()          { n.send("READY=1") }
func (n *Notifier) Stopping()       { n.send("STOPPING=1") }
func (n *Notifier) Status(s string) { n.send("STATUS=" + s) }
func (n *Notifier) Watchdog()       { n.send("WATCHDOG=1") }

type Heartbeat struct{ last atomic.Int64 }

func NewHeartbeat() *Heartbeat {
	h := &Heartbeat{}
	h.Beat()
	return h
}

func (h *Heartbeat) Beat() { h.last.Store(time.Now().UnixNano()) }

func (h *Heartbeat) Fresh(maxAge time.Duration) bool {
	return time.Since(time.Unix(0, h.last.Load())) <= maxAge
}

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

func (n *Notifier) Watch(ctx context.Context, h *Heartbeat) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	go n.RunWatchdog(ctx, func() bool { return h.Fresh(n.interval * 2) })
	return cancel
}
