package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	SourceProxy = "proxy"
	SourceLocal = "local"
	SourceOIDC  = "oidc"

	LoginPath    = "/login"
	CallbackPath = oidc.CallbackPath
	LogoutPath   = "/logout"

	sessionCookie = "atrium_session"
	bindingCookie = "atrium_oidc_state"
	hostPrefix    = "__Host-"
)

var (
	errAnonymous   = errors.New("no identity")
	errExpired     = errors.New("session expired")
	errRevoked     = errors.New("session revoked")
	errUnavailable = errors.New("session lookup failed")
)

type Identity struct {
	Source  string
	Subject string
	Email   string
	Name    string
	Admin   bool
	NoAuth  bool
}

type identityKey struct{}

func IdentityOf(ctx context.Context) (Identity, bool) {
	who, ok := ctx.Value(identityKey{}).(Identity)
	return who, ok
}

func signInPath(r *http.Request) bool {
	switch r.URL.Path {
	case LoginPath:
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	case CallbackPath:
		return r.Method == http.MethodGet
	}
	return false
}

func (g *Guard) identify(r *http.Request) (Identity, error) {
	if g.auth.Local {
		return Identity{Source: SourceLocal, Admin: true}, nil
	}
	if g.auth.Proxy && hasGroup(r.Header.Get("Remote-Groups"), Group) {
		return Identity{Source: SourceProxy, Admin: hasGroup(r.Header.Get("Remote-Groups"), g.adminGroup(r.Context()))}, nil
	}
	if g.auth.OIDC {
		if token := cookieValue(r, sessionCookie); token != "" {
			s, err := g.sessions.Authenticate(r.Context(), token)
			switch {
			case err == nil:
				return Identity{Source: SourceOIDC, Subject: s.Subject, Email: s.Email, Name: s.Name, Admin: s.Admin}, nil
			case errors.Is(err, oidc.ErrRevoked):
				return Identity{}, errRevoked
			case errors.Is(err, oidc.ErrNoSession):
				return Identity{}, errExpired
			default:
				log.Error("session lookup failed", logx.Err, err.Error())
				return Identity{}, errUnavailable
			}
		}
	}
	return Identity{}, errAnonymous
}

func (g *Guard) unauthenticated(w http.ResponseWriter, r *http.Request, why error) {
	if !g.auth.OIDC {
		deny(w, http.StatusForbidden, "Forbidden")
		return
	}
	setSecurityHeaders(w.Header())
	w.Header().Set("Cache-Control", "no-store")
	q := url.Values{}
	switch {
	case errors.Is(why, errRevoked):
		q.Set("reason", "revoked")
	case errors.Is(why, errExpired):
		q.Set("reason", "expired")
	}
	if !errors.Is(why, errAnonymous) {
		expireCookie(w, sessionCookie, isHTTPS(r))
	}
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	if get && !IsPartial(r) {
		if next := oidc.SafeReturn(r.URL.RequestURI()); next != "/" {
			q.Set("next", next)
		}
	}
	target := LoginPath
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	if IsPartial(r) {
		w.Header().Set("HX-Redirect", target)
		deny(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func cookieValue(r *http.Request, name string) string {
	for _, candidate := range []string{hostPrefix + name, name} {
		if c, err := r.Cookie(candidate); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	if secure {
		name = hostPrefix + name
	}
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	if maxAge < 0 {
		c.Value = ""
	}
	http.SetCookie(w, c)
}

func expireCookie(w http.ResponseWriter, name string, secure bool) {
	setCookie(w, name, "", -1, secure)
	if secure {
		setCookie(w, name, "", -1, false)
	}
}

func hasSecureTransport(r *http.Request, publicSecure bool) bool { return publicSecure || isHTTPS(r) }

func routeMatches(pattern, path string) bool {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(strings.ToLower(path), "/"), "/")
	for i, seg := range want {
		if seg == "*" {
			return true
		}
		if i >= len(got) || got[i] != seg {
			return false
		}
	}
	return len(got) == len(want)
}
