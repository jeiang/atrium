package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	AuthProxy = "proxy"
	AuthLocal = "local"
	AuthOIDC  = "oidc"

	Group      = "atrium_access"
	CSRFCookie = "atrium_csrf"
	CSRFHeader = "X-CSRF-Token"
	CSRFField  = "csrf_token"

	StaticPrefix = "/static/"
	HealthPath   = "/healthz"

	CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'"

	formType = "application/x-www-form-urlencoded"
)

var (
	AuthModes  = [...]string{AuthProxy, AuthLocal, AuthOIDC}
	localHosts = [...]string{"localhost", "127.0.0.1", "[::1]"}
)

type csrfKey struct{}

func CSRFToken(ctx context.Context) string {
	token, _ := ctx.Value(csrfKey{}).(string)
	return token
}

type bodyCapKey struct{}

const defaultBodyCap = 1 << 20

func bodyCap(ctx context.Context) int {
	if n, ok := ctx.Value(bodyCapKey{}).(int); ok {
		return n
	}
	return defaultBodyCap
}

type Guard struct {
	auth     AuthSet
	hosts    map[string]struct{}
	public   func(*http.Request) bool
	bodyCap  func(context.Context) (int, error)
	sessions *oidc.Service
	errPage  ErrorPage
	noAuth   bool
	admin    func(context.Context) string
	secure   func(*http.Request) bool
}

func NewGuard(auth AuthSet, allowedHosts []string, public func(*http.Request) bool) *Guard {
	hosts := map[string]struct{}{}
	switch {
	case len(allowedHosts) > 0:
		for _, h := range allowedHosts {
			hosts[hostname(h)] = struct{}{}
		}
	case auth.Local:
		for _, h := range localHosts {
			hosts[h] = struct{}{}
		}
	}
	if public == nil {
		public = func(*http.Request) bool { return false }
	}
	return &Guard{auth: auth, hosts: hosts, public: public}
}

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

func hasGroup(value, group string) bool {
	for _, part := range strings.Split(value, ",") {
		if py.Strip(part) == group {
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
	token, found := "", false
	for _, line := range r.Header.Values("Cookie") {
		for pair := range strings.SplitSeq(line, ";") {
			name, value, ok := strings.Cut(pair, "=")
			if !ok || py.Strip(name) != CSRFCookie {
				continue
			}
			value = py.Strip(value)
			if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
				value = value[1 : len(value)-1]
			}
			token, found = value, true
		}
	}
	return token, found
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
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

func healthProbe(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.EscapedPath() == HealthPath
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
		static := strings.HasPrefix(r.URL.Path, StaticPrefix)
		who, err := g.identify(r)
		switch {
		case errors.Is(err, errUnavailable):
			deny(w, http.StatusServiceUnavailable, "Service Unavailable")
			return
		case err != nil && !static && !g.public(r) && (!g.auth.OIDC || !signInPath(r)):
			g.unauthenticated(w, r, err)
			return
		}
		token, found := cookieToken(r)
		fresh := !found
		if fresh {
			token = newToken()
		}
		if err == nil {
			who.NoAuth = who.Source == SourceLocal && g.noAuth
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, who))
			if !who.Admin && adminRoute(r) {
				setSecurityHeaders(w.Header())
				g.issueCSRF(w, r, token, fresh)
				r = r.WithContext(context.WithValue(context.WithValue(r.Context(), csrfKey{}, token), adminOnlyKey{}, true))
				if g.errPage != nil {
					g.errPage(w, r, http.StatusForbidden, "Admins only.")
				} else {
					deny(w, http.StatusForbidden, "Forbidden: administrator access required")
				}
				return
			}
		}
		if isUnsafe(r.Method) {
			limit := defaultBodyCap
			if g.bodyCap != nil {
				var err error
				if limit, err = g.bodyCap(r.Context()); err != nil {
					log.Error("body cap setting failed", logx.Err, err.Error())
					deny(w, http.StatusInternalServerError, "Internal Server Error")
					return
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), bodyCapKey{}, limit))
			if fresh || !sameOrigin(r) {
				deny(w, http.StatusForbidden, "Forbidden: cross-site request")
				return
			}
			supplied, has := "", false
			if values := r.Header.Values(CSRFHeader); len(values) > 0 {
				supplied, has = values[0], true
			} else if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); py.Lower(py.Strip(mediaType)) == formType {
				body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
				if err != nil || len(body) > limit {
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
		setSecurityHeaders(w.Header())
		g.issueCSRF(w, r, token, fresh)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfKey{}, token)))
	})
}

func (g *Guard) adminGroup(ctx context.Context) string {
	if g.admin == nil {
		return oidc.DefaultAdminGroup
	}
	return g.admin(ctx)
}

func (g *Guard) issueCSRF(w http.ResponseWriter, r *http.Request, token string, fresh bool) {
	if !fresh {
		return
	}
	cookie := CSRFCookie + "=" + token + "; Path=/; HttpOnly; SameSite=Strict"
	if isHTTPS(r) {
		cookie += "; Secure"
	}
	w.Header().Add("Set-Cookie", cookie)
}

type adminOnlyKey struct{}

func AdminOnly(ctx context.Context) bool {
	v, _ := ctx.Value(adminOnlyKey{}).(bool)
	return v
}
