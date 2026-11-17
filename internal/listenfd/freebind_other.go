//go:build !linux

package listenfd

import "net"

func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
