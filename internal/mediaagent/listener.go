//go:build linux || darwin

package mediaagent

import (
	"net"
	"sync"
)

type boundedListener struct {
	net.Listener
	slots   chan struct{}
	done    chan struct{}
	once    sync.Once
	allowed func(string) bool
}

func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			<-l.slots
			return nil, err
		}
		if !l.allowed(c.RemoteAddr().String()) {
			_ = c.Close()
			continue
		}
		return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
	}
}
func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
