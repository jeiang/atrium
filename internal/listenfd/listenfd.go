package listenfd

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

func Listen(fd, addr string) (net.Listener, error) {
	if fd == "" {
		return net.Listen("tcp", addr)
	}
	n, err := strconv.Atoi(fd)
	if err != nil || n < 3 {
		return nil, fmt.Errorf("listener descriptor %q is not an inherited descriptor", fd)
	}
	f := os.NewFile(uintptr(n), "listener")
	defer func() { _ = f.Close() }()
	l, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("listener descriptor %d: %w", n, err)
	}
	want, _, _ := net.SplitHostPort(addr)
	got, _, _ := net.SplitHostPort(l.Addr().String())
	if ip := net.ParseIP(want); ip != nil && !ip.IsUnspecified() && !ip.Equal(net.ParseIP(got)) {
		_ = l.Close()
		return nil, fmt.Errorf("listener descriptor %d is bound to %s, not the configured host %s", n, got, want)
	}
	return l, nil
}
