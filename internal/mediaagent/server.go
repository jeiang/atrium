//go:build linux || darwin

package mediaagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const (
	shutdownGrace = 10 * time.Second
	handlerGrace  = 5 * time.Second
	bodyDeadline  = 15 * time.Second
)

type Agent struct {
	cfg         Config
	roots       *roots
	state       *state
	token       [sha256.Size]byte
	tls         *tls.Config
	hosts       map[string]bool
	peers       []netip.Prefix
	draining    atomic.Bool
	inflight    sync.WaitGroup
	settled     atomic.Bool
	gid         int
	receiptMu   sync.Mutex
	mutationMu  sync.Mutex
	receipts    map[string]*receipt
	quarantined []string
	imports     importSnapshots
	library     librarySnapshots
	thumbs      *thumbnailer
	work        context.Context
	stopWork    context.CancelFunc
	filings     sync.WaitGroup
}

func New(c Config) (*Agent, error) {
	token, err := readToken(c.Control.TokenFile)
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: c, token: sha256.Sum256(token), hosts: map[string]bool{}, peers: c.Control.AllowedPeers}
	clear(token)
	a.gid = -1
	if c.Group != "" {
		a.gid, err = lookupGroup(c.Group)
		if err != nil {
			return nil, err
		}
		if a.gid != os.Getegid() {
			groups, err := os.Getgroups()
			if err != nil || !slices.Contains(groups, a.gid) {
				return nil, fmt.Errorf("the media agent is not a member of group %q", c.Group)
			}
		}
	}
	for _, h := range c.Control.AllowedHosts {
		a.hosts[h] = true
	}
	if c.Control.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.Control.TLSCert, c.Control.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("load control TLS certificate: %w", err)
		}
		a.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	}
	if a.state, err = openState(c.StateDirectory, c.CacheDirectory); err != nil {
		return nil, err
	}
	if a.roots, err = openRoots(c); err != nil {
		_ = a.state.close()
		return nil, err
	}
	if err := a.loadReceipts(); err != nil {
		a.roots.close()
		_ = a.state.close()
		return nil, err
	}
	a.thumbs = newThumbnailer()
	pruneThumbnails(filepath.Join(c.CacheDirectory, "thumbs"))
	a.work, a.stopWork = context.WithCancel(context.Background())
	a.settled.Store(true)
	return a, nil
}

func (a *Agent) Close() error {
	if !a.settled.Load() {
		return nil
	}
	a.roots.close()
	return a.state.close()
}

func (a *Agent) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", a.cfg.Control.Listen)
	if err != nil {
		return nil, err
	}
	return &boundedListener{Listener: ln, slots: make(chan struct{}, 256), done: make(chan struct{}), allowed: a.peerAllowed}, nil
}

func (a *Agent) Serve(ctx context.Context, ln net.Listener, stopping func()) error {
	srv := &http.Server{MaxHeaderBytes: 16 << 10, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() {
		if a.tls != nil {
			srv.TLSConfig = a.tls
			errc <- srv.ServeTLS(ln, "", "")
			return
		}
		errc <- srv.Serve(ln)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	a.draining.Store(true)
	a.stopWork()
	if stopping != nil {
		stopping()
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	err := srv.Shutdown(sctx)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		err = srv.Close()
	}
	if serr := <-errc; !errors.Is(serr, http.ErrServerClosed) && err == nil {
		err = serr
	}
	done := make(chan struct{})
	go func() {
		a.inflight.Wait()
		a.filings.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(handlerGrace):
		a.settled.Store(false)
		if err == nil {
			err = errors.New("media agent requests did not finish after shutdown")
		}
	}
	return err
}

func (a *Agent) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.inflight.Add(1)
		defer a.inflight.Done()
		if !a.hosts[r.Host] {
			problem(w, http.StatusBadRequest, protocol.CodeInvalidRequest, "the Host header is not allowed")
			return
		}
		if !a.peerAllowed(r.RemoteAddr) {
			problem(w, http.StatusForbidden, protocol.CodeForbidden, "the peer address is not allowed")
			return
		}
		if !a.authorized(r.Header.Values("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="atrium-media-agent"`)
			problem(w, http.StatusUnauthorized, protocol.CodeUnauthorized, "a valid bearer token is required")
			return
		}
		switch r.URL.Path {
		case "/health":
			if allowRead(w, r) {
				a.serveHealth(w)
			}
		case "/v1/files":
			if allowRead(w, r) {
				a.serveFile(w, r)
			}
		default:
			if !a.filingRoutes(w, r) && !a.libraryRoutes(w, r) {
				problem(w, http.StatusNotFound, protocol.CodeNotFound, "no such endpoint")
			}
		}
	})
}

func allowRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	problem(w, http.StatusMethodNotAllowed, protocol.CodeMethodNotAllowed, "only GET and HEAD are allowed")
	return false
}

func (a *Agent) peerAllowed(remote string) bool {
	if len(a.peers) == 0 {
		return true
	}
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	addr := ap.Addr().Unmap()
	for _, p := range a.peers {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (a *Agent) authorized(values []string) bool {
	if len(values) != 1 {
		return false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], a.token[:]) == 1
}

func (a *Agent) serveHealth(w http.ResponseWriter) {
	h := protocol.Health{
		Protocol:     protocol.Version,
		AgentID:      a.state.agentID,
		RootRevision: a.roots.revision,
		Capabilities: a.capabilities(),
		Ready:        true,
	}
	switch {
	case a.draining.Load():
		h.Ready, h.ReasonCode = false, protocol.CodeDraining
	case !a.roots.healthy():
		h.Ready, h.ReasonCode = false, protocol.CodeRootChanged
	}
	status := http.StatusOK
	if !h.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, h)
}

func problem(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.Problem{Code: code, Message: message, Mutation: protocol.MutationNone})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Agent) capabilities() []string {
	c := []string{protocol.CapabilityFile, protocol.CapabilityScan, protocol.CapabilityStream}
	if a.thumbs != nil {
		c = append(c, protocol.CapabilityThumbnail)
	}
	return c
}
