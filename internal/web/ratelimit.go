package web

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/auth/oidc"
)

const (
	limitKeys = 10000
	limitIdle = 15 * time.Minute

	limitAPIAddressRate  = 120.0 / 60
	limitAPIAddressBurst = 120
	limitAPITokenRate    = 60.0 / 60
	limitAPITokenBurst   = 60
	limitSignInRate      = 10.0 / 60
	limitSignInBurst     = 10
)

const (
	classAPIAddress = iota
	classAPIToken
	classSignIn
)

type limitClass struct {
	rate  float64
	burst float64
}

type limitCharge struct {
	class int
	key   string
}

type limitBucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu      sync.Mutex
	classes []limitClass
	buckets []map[string]*limitBucket
	now     func() time.Time
}

func newLimiter() *limiter {
	return &limiter{
		classes: []limitClass{
			{rate: limitAPIAddressRate, burst: limitAPIAddressBurst},
			{rate: limitAPITokenRate, burst: limitAPITokenBurst},
			{rate: limitSignInRate, burst: limitSignInBurst},
		},
		buckets: []map[string]*limitBucket{{}, {}, {}},
		now:     time.Now,
	}
}

func (b *limitBucket) refill(now time.Time, c limitClass) float64 {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(c.burst, b.tokens+elapsed.Seconds()*c.rate)
		b.last = now
	}
	return b.tokens
}

func (l *limiter) charge(now time.Time, charges ...limitCharge) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var retry time.Duration
	for _, c := range charges {
		wait, ok := l.peek(now, c)
		if ok {
			continue
		}
		if wait > retry {
			retry = wait
		}
	}
	if retry > 0 {
		return retry, false
	}
	for _, c := range charges {
		l.take(now, c)
	}
	return 0, true
}

func (l *limiter) peek(now time.Time, c limitCharge) (time.Duration, bool) {
	entries := l.buckets[c.class]
	b, ok := entries[c.key]
	if !ok {
		if len(entries) < limitKeys || l.evict(now, entries) {
			return 0, true
		}
		return limitIdle, false
	}
	tokens := b.refill(now, l.classes[c.class])
	if tokens >= 1 {
		return 0, true
	}
	wait := (1 - tokens) / l.classes[c.class].rate
	return time.Duration(math.Ceil(wait)) * time.Second, false
}

func (l *limiter) take(now time.Time, c limitCharge) {
	entries := l.buckets[c.class]
	b, ok := entries[c.key]
	if !ok {
		b = &limitBucket{tokens: l.classes[c.class].burst, last: now}
		entries[c.key] = b
	}
	b.refill(now, l.classes[c.class])
	if b.tokens >= 1 {
		b.tokens--
	}
	b.last = now
}

func (l *limiter) evict(now time.Time, entries map[string]*limitBucket) bool {
	cutoff := now.Add(-limitIdle)
	removed := false
	for key, b := range entries {
		if b.last.Before(cutoff) {
			delete(entries, key)
			removed = true
		}
	}
	return removed
}

func (g *Guard) clientAddr(r *http.Request) (netip.Addr, bool) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	addr := peer.Addr().Unmap()
	if !g.trustedPeer(addr) {
		return addr, true
	}
	values := r.Header.Values("CF-Connecting-IP")
	switch len(values) {
	case 0:
		return addr, true
	case 1:
	default:
		return netip.Addr{}, false
	}
	forwarded := strings.TrimSpace(values[0])
	if forwarded == "" || strings.Contains(forwarded, ",") {
		return netip.Addr{}, false
	}
	addr, err = netip.ParseAddr(forwarded)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (g *Guard) trustedPeer(addr netip.Addr) bool {
	for _, prefix := range g.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func boundedToken(token string) bool {
	if len(token) > oidc.MaxTokenBytes {
		return false
	}
	for i := range len(token) {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

func retryAfter(w http.ResponseWriter, wait time.Duration) {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

func (g *Guard) limitSignIn(w http.ResponseWriter, r *http.Request) bool {
	addr, ok := g.clientAddr(r)
	if !ok {
		deny(w, http.StatusBadRequest, "Bad forwarded address")
		return false
	}
	wait, allowed := g.limits.charge(g.limits.now(), limitCharge{class: classSignIn, key: addr.String()})
	if allowed {
		return true
	}
	setSecurityHeaders(w.Header())
	w.Header().Set("Cache-Control", "no-store")
	retryAfter(w, wait)
	g.errPage(w, r, http.StatusTooManyRequests, "Too many requests.")
	return false
}

func signInRequest(r *http.Request) bool {
	switch r.URL.Path {
	case loginPath:
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	case callbackPath:
		return r.Method == http.MethodGet
	}
	return false
}
