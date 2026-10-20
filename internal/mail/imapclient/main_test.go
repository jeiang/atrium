package imapclient

import (
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
)

type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if ip := net.ParseIP(r.URL.Hostname()); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("offline")
	}
	return l.next.RoundTrip(r)
}

func TestMain(m *testing.M) {
	http.DefaultTransport = loopbackOnly{http.DefaultTransport}
	os.Exit(m.Run())
}
