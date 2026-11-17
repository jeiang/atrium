//go:build linux

package listenfd

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func listenTCP(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(network, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			if network == "tcp6" {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_IPV6, unix.IPV6_FREEBIND, 1)
			} else {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_IP, unix.IP_FREEBIND, 1)
			}
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), "tcp", addr)
}
