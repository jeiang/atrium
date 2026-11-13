//go:build linux || darwin

package mediaagent

import (
	"net"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const vpnPrefix = "/library/"

func (a *Agent) ListenVPN() (net.Listener, error) {
	if a.cfg.VPN == nil {
		return nil, nil
	}
	ln, err := net.Listen("tcp", a.cfg.VPN.Listen)
	if err != nil {
		return nil, err
	}
	return &boundedListener{Listener: ln, slots: make(chan struct{}, 256), done: make(chan struct{}), allowed: func(string) bool { return true }}, nil
}

func (a *Agent) VPNHandler() http.Handler {
	hosts := map[string]bool{}
	for _, h := range a.cfg.VPN.AllowedHosts {
		hosts[h] = true
	}
	peers := a.cfg.VPN.AllowedPeers
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.inflight.Add(1)
		defer a.inflight.Done()
		if !hosts[r.Host] {
			problem(w, http.StatusBadRequest, protocol.CodeInvalidRequest, "the Host header is not allowed")
			return
		}
		if !peerIn(peers, r.RemoteAddr) {
			problem(w, http.StatusForbidden, protocol.CodeForbidden, "the peer address is not allowed")
			return
		}
		root, rel, ok := vpnPath(r.URL.Path)
		if !ok {
			problem(w, http.StatusNotFound, protocol.CodeNotFound, "no such endpoint")
			return
		}
		if allowRead(w, r) {
			a.streamFile(w, r, root, rel)
		}
	})
}

func vpnPath(p string) (string, string, bool) {
	rest, ok := strings.CutPrefix(p, vpnPrefix)
	if !ok {
		return "", "", false
	}
	root, rel, ok := strings.Cut(rest, "/")
	if !ok || rel == "" || (root != protocol.RootMovies && root != protocol.RootTV) {
		return "", "", false
	}
	return root, rel, true
}
