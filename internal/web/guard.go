package web

import (
	"context"

	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/jeiang/atrium/internal/application"

	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	AuthProxy = "proxy"
	AuthLocal = "local"
	AuthOIDC  = "oidc"

	group      = "atrium_access"
	CSRFCookie = "atrium_csrf"
	CSRFHeader = "X-CSRF-Token"
	csrfField  = "csrf_token"

	staticPrefix = "/static/"
	healthPath   = "/healthz"

	csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; worker-src 'self'"

	formType = "application/x-www-form-urlencoded"
)

var (
	authModes  = [...]string{AuthProxy, AuthLocal, AuthOIDC}
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
	auth     Policy
	hosts    map[string]struct{}
	trusted  []netip.Prefix
	limits   *limiter
	public   func(*http.Request) bool
	bodyCap  func(context.Context) (int, error)
	apiShape func(*http.Request) *application.APIShape
	sessions *authapp.Sessions
	bearers  *authapp.Service
	errPage  ErrorPage
	secure   func(*http.Request) bool
	classify func(*http.Request) access
}

func newGuard(auth Policy, allowedHosts []string, trusted []netip.Prefix, public func(*http.Request) bool, classify func(*http.Request) access) *Guard {
	hosts := map[string]struct{}{}
	switch {
	case len(allowedHosts) > 0:
		for _, h := range allowedHosts {
			hosts[hostname(h)] = struct{}{}
		}
	case auth.Local():
		for _, h := range localHosts {
			hosts[h] = struct{}{}
		}
	}
	if public == nil {
		public = func(*http.Request) bool { return false }
	}
	return &Guard{auth: auth, hosts: hosts, trusted: trusted, limits: newLimiter(), public: public, classify: classify}
}

func hostname(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
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
		if strings.TrimSpace(part) == group {
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
	return r.Method == http.MethodGet && r.URL.EscapedPath() == healthPath
}

func loopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.auth.Local() && !loopbackPeer(r.RemoteAddr) {
			deny(w, http.StatusForbidden, "Forbidden")
			return
		}
		if healthProbe(r) {
			setSecurityHeaders(w.Header())
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := g.hosts[hostname(r.Host)]; !ok {
			deny(w, http.StatusBadRequest, "Bad Host header")
			return
		}
		if isAPI(r) {
			g.api(w, r, next)
			return
		}
		if g.auth.OIDC() && signInRequest(r) && !g.limitSignIn(w, r) {
			return
		}
		static := strings.HasPrefix(r.URL.Path, staticPrefix)
		class := accessDefault
		if g.classify != nil {
			class = g.classify(r)
		}
		who, err := g.identify(r)
		switch {
		case errors.Is(err, errUnavailable):
			deny(w, http.StatusServiceUnavailable, "Service Unavailable")
			return
		case err != nil && !static && !g.public(r) && class != accessPublic:
			g.unauthenticated(w, r, err)
			return
		}
		token, found := "", false
		if c, err := r.Cookie(CSRFCookie); err == nil {
			token, found = c.Value, true
		}
		fresh := !found
		if fresh {
			token = newToken()
		}
		if err == nil {
			r = r.WithContext(withIdentity(r.Context(), who, audit.UI))
			r = r.WithContext(g.sessionAccess(r, who, token))
		}
		if isUnsafe(r.Method) && !strings.HasPrefix(r.URL.Path, "/actions/") {
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
			} else if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.ToLower(strings.TrimSpace(mediaType)) == formType {
				r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
				if err := r.ParseForm(); err != nil {
					var tooLarge *http.MaxBytesError
					if errors.As(err, &tooLarge) {
						deny(w, http.StatusRequestEntityTooLarge, "Request too large")
					} else {
						deny(w, http.StatusForbidden, "Forbidden: missing or stale form token")
					}
					return
				}
				supplied, has = r.PostForm.Get(csrfField), r.PostForm.Has(csrfField)
				if supplied == "" {
					has = false
				}
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

func withIdentity(ctx context.Context, who Identity, source audit.Source) context.Context {
	return audit.With(authapp.WithPrincipal(ctx, who), "operator", source)
}

func withAPIIdentity(ctx context.Context, who Identity) context.Context {
	return withIdentity(ctx, who, audit.API)
}
