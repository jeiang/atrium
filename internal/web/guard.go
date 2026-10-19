package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	AuthProxy = "proxy"
	AuthLocal = "local"

	Group      = "atrium_access"
	CSRFCookie = "atrium_csrf"
	CSRFHeader = "X-CSRF-Token"
	CSRFField  = "csrf_token"

	StaticPrefix = "/static/"
	HealthPath   = "/healthz"
	BodyCap      = 1 << 20

	CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'"

	formType = "application/x-www-form-urlencoded"
)

var (
	AuthModes  = [...]string{AuthProxy, AuthLocal}
	localHosts = [...]string{"localhost", "127.0.0.1", "[::1]"}
)

type csrfKey struct{}

// CSRFToken is the request's CSRF token for templates and handlers; it is
// empty on a request the Guard did not admit.
func CSRFToken(ctx context.Context) string {
	token, _ := ctx.Value(csrfKey{}).(string)
	return token
}

// Guard is the Python auth.Guard: Host allowlist, Remote-Groups gate, CSRF,
// same-origin and security headers.
type Guard struct {
	auth   string
	hosts  map[string]struct{}
	public func(*http.Request) bool
}

// NewGuard builds the Guard. A non-empty allowedHosts replaces the loopback
// defaults of local mode; proxy mode has no default, so an unconfigured
// proxy Guard answers 400 to every request. public admits a request without
// Remote-Groups (the signed media links); the Host check still applies.
func NewGuard(auth string, allowedHosts []string, public func(*http.Request) bool) *Guard {
	hosts := map[string]struct{}{}
	switch {
	case len(allowedHosts) > 0:
		for _, h := range allowedHosts {
			hosts[hostname(h)] = struct{}{}
		}
	case auth == AuthLocal:
		for _, h := range localHosts {
			hosts[h] = struct{}{}
		}
	}
	if public == nil {
		public = func(*http.Request) bool { return false }
	}
	return &Guard{auth: auth, hosts: hosts, public: public}
}

// hostname is the Host header without its port; bracketed IPv6 literals stay
// bracketed.
func hostname(value string) string {
	value = py.Lower(py.Strip(value))
	if strings.HasPrefix(value, "[") {
		if end := strings.IndexByte(value, ']'); end >= 0 {
			return value[:end+1]
		}
		return value
	}
	host, _, _ := strings.Cut(value, ":")
	return host
}

func hasGroup(value string) bool {
	for _, part := range strings.Split(value, ",") {
		if py.Strip(part) == Group {
			return true
		}
	}
	return false
}

func isUnsafe(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func cookieToken(r *http.Request) (string, bool) {
	raw := r.Header.Get("Cookie")
	if raw == "" {
		return "", false
	}
	cookies, _ := http.ParseCookie(raw)
	token, found := "", false
	for _, c := range cookies {
		if c.Name == CSRFCookie {
			token, found = c.Value, true
		}
	}
	return token, found
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func deny(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// healthProbe is exempt from the Host and group checks: the unit and the
// proxy probe it by address and carry no Remote-Groups.
func healthProbe(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == HealthPath
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthProbe(r) {
			setSecurityHeaders(w.Header())
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := g.hosts[hostname(r.Host)]; !ok {
			deny(w, http.StatusBadRequest, "Bad Host header")
			return
		}
		if g.auth == AuthProxy && !strings.HasPrefix(r.URL.Path, StaticPrefix) && !hasGroup(r.Header.Get("Remote-Groups")) && !g.public(r) {
			deny(w, http.StatusForbidden, "Forbidden")
			return
		}
		token, found := cookieToken(r)
		fresh := !found
		if fresh {
			token = newToken()
		}
		if isUnsafe(r.Method) {
			if fresh || !sameOrigin(r) {
				deny(w, http.StatusForbidden, "Forbidden: cross-site request")
				return
			}
			supplied, has := "", false
			if values := r.Header.Values(CSRFHeader); len(values) > 0 {
				supplied, has = values[0], true
			} else if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); py.Lower(py.Strip(mediaType)) == formType {
				body, err := io.ReadAll(io.LimitReader(r.Body, BodyCap+1))
				if err != nil || len(body) > BodyCap {
					deny(w, http.StatusRequestEntityTooLarge, "Request too large")
					return
				}
				supplied, has = formField(string(body), CSRFField)
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			if !has || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
				deny(w, http.StatusForbidden, "Forbidden: missing or stale form token")
				return
			}
		}
		h := w.Header()
		setSecurityHeaders(h)
		if fresh {
			secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			cookie := CSRFCookie + "=" + token + "; Path=/; HttpOnly; SameSite=Strict"
			if secure {
				cookie += "; Secure"
			}
			h.Add("Set-Cookie", cookie)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfKey{}, token)))
	})
}
