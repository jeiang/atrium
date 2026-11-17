package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"

	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/urlx"
)

const (
	SourceProxy = "proxy"
	SourceLocal = "local"
	SourceOIDC  = "oidc"

	loginPath    = "/login"
	callbackPath = oidc.CallbackPath
	logoutPath   = "/logout"

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

type Identity = authapp.Principal

func IdentityOf(ctx context.Context) (Identity, bool) {
	return authapp.PrincipalOf(ctx)
}

func (g *Guard) identify(r *http.Request) (Identity, error) {
	agent := markedAgent(r)
	if g.auth.Local() {
		return Identity{Source: SourceLocal, Agent: agent}, nil
	}
	if g.auth.Proxy() {
		if _, trusted, _ := g.peer(r); trusted && hasGroup(r.Header.Get("Remote-Groups"), group) {
			return Identity{Source: SourceProxy, Agent: agent}, nil
		}
	}
	if g.auth.OIDC() {
		if token := cookieValue(r, sessionCookie, g.secure(r)); token != "" {
			s, err := g.sessions.Authenticate(r.Context(), token)
			if err == nil && agent && !s.Agent {
				if err = g.sessions.MarkAgent(r.Context(), token); err == nil {
					s.Agent = true
				}
			}
			switch {
			case err == nil:
				return Identity{Source: SourceOIDC, Subject: s.Subject, Email: s.Email, Name: s.Name, Agent: s.Agent}, nil
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

func markedAgent(r *http.Request) bool { return r.Header.Get(AgentHeader) == "1" }

func (g *Guard) unauthenticated(w http.ResponseWriter, r *http.Request, why error) {
	if !g.auth.OIDC() {
		deny(w, http.StatusForbidden, "Forbidden")
		return
	}
	setSecurityHeaders(w.Header())
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(why, errAnonymous) && crossSiteNavigation(r) {
		continuePage(w, r)
		return
	}
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
	if get {
		if next := oidc.SafeReturn(r.URL.RequestURI()); next != "/" {
			q.Set("next", next)
		}
	}
	target := loginPath
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func cookieValue(r *http.Request, name string, secure bool) string {
	if secure {
		name = hostPrefix + name
	}
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	site := http.SameSiteStrictMode
	if name == bindingCookie {
		site = http.SameSiteLaxMode
	}
	if secure {
		name = hostPrefix + name
	}
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: site, MaxAge: maxAge}
	if maxAge < 0 {
		c.Value = ""
	}
	http.SetCookie(w, c)
}

func expireCookie(w http.ResponseWriter, name string, secure bool) {
	setCookie(w, name, "", -1, secure)
}

func hasSecureTransport(r *http.Request, publicSecure bool) bool { return publicSecure || isHTTPS(r) }

const continueDocument = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="refresh" content="0;url=%[1]s"><title>Atrium</title></head>
<body><p><a href="%[1]s">Continue to Atrium</a></p></body></html>
`

func crossSiteNavigation(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.Header.Get("Sec-Fetch-Mode") == "navigate"
}

func continuePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = fmt.Fprintf(w, continueDocument, html.EscapeString(urlx.SafeReturn(r.URL.RequestURI())))
	}
}
