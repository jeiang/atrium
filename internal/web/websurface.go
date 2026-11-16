package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	ClientHeader = "Atrium-Client"
	ClientWeb    = "web"
	AgentHeader  = "Atrium-Agent"

	WebSessionPath = WebPrefix + "/session"
	WebLogoutPath  = WebPrefix + "/session/logout"
)

type WebSession struct {
	AuthMode  string      `json:"auth_mode"`
	Identity  Identity    `json:"identity"`
	CSRFToken string      `json:"csrf_token"`
	SignOut   *WebSignOut `json:"sign_out" api:"required"`
}

type WebSignOut struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

type WebSignIn struct {
	Modes     []string `json:"modes"`
	LoginURL  string   `json:"login_url,omitempty"`
	CSRFToken string   `json:"csrf_token"`
}

func (p Policy) modes() []string {
	var out []string
	if p.Local() {
		out = append(out, AuthLocal)
	}
	if p.Proxy() {
		out = append(out, AuthProxy)
	}
	if p.OIDC() {
		out = append(out, AuthOIDC)
	}
	return out
}

func (g *Guard) web(w http.ResponseWriter, r *http.Request, next http.Handler) {
	h := w.Header()
	setSecurityHeaders(h)
	h.Set("Cache-Control", "no-store")
	h.Set(RequestIDHeader, rand.Text())
	if _, ok := r.Header["Authorization"]; ok {
		APIError(w, http.StatusBadRequest, "bearer_refused", "The browser surface takes no Authorization header. Bearer tokens use /api/v1.")
		return
	}
	addr, addrOK := g.clientAddr(r)
	if !addrOK {
		APIError(w, http.StatusBadRequest, "bad_request", "The forwarded address is not valid.")
		return
	}
	browser, fresh := "", true
	if c, err := r.Cookie(CSRFCookie); err == nil && c.Value != "" {
		browser, fresh = c.Value, false
	}
	charges := []limitCharge{{class: classAPIAddress, key: addr.String()}}
	if key := g.webSessionKey(r, browser); key != "" {
		charges = append(charges, limitCharge{class: classAPIToken, key: key})
	}
	if wait, allowed := g.limits.charge(g.limits.now(), charges...); !allowed {
		retryAfter(w, wait)
		APIError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests.")
		return
	}
	shape := g.shapeOf(r)
	binary := (r.Method == http.MethodGet || r.Method == http.MethodHead) && shape != nil && shape.Body == application.BodyBinary
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && (!binary || site != "none") {
		APIError(w, http.StatusForbidden, "cross_origin", "Cross-site requests are refused.")
		return
	}
	if !binary && r.Header.Get(ClientHeader) != ClientWeb {
		APIError(w, http.StatusForbidden, "cross_origin", "Send the Atrium-Client: web header.")
		return
	}
	if fresh {
		browser = newToken()
	}
	who, err := g.identify(r)
	switch {
	case errors.Is(err, errUnavailable):
		APIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Sign-in is unavailable.")
		return
	case err != nil:
		g.issueCSRF(w, r, browser, fresh)
		g.webUnauthenticated(w, r, err, browser)
		return
	}
	if isUnsafe(r.Method) {
		if !webSameOrigin(r) {
			APIError(w, http.StatusForbidden, "cross_origin", "Cross-origin requests are refused.")
			return
		}
		supplied := r.Header.Get(CSRFHeader)
		if fresh || supplied == "" || subtle.ConstantTimeCompare([]byte(supplied), []byte(browser)) != 1 {
			APIError(w, http.StatusForbidden, "csrf_failed", "Send the csrf_token from GET /web/v1/session in the X-CSRF-Token header.")
			return
		}
	}
	if !g.admitBody(w, r, shape) {
		return
	}
	ctx := context.WithValue(withIdentity(r.Context(), who, audit.UI), originKey{}, Origin(r))
	r = r.WithContext(context.WithValue(ctx, csrfKey{}, browser))
	r = r.WithContext(g.sessionAccess(r, who, browser))
	g.issueCSRF(w, r, browser, fresh)
	next.ServeHTTP(w, r)
}

func (g *Guard) webSessionKey(r *http.Request, browser string) string {
	key := browser
	if g.auth.OIDC() {
		if session := cookieValue(r, sessionCookie, g.secure(r)); session != "" {
			key = session
		}
	}
	if key == "" || !boundedToken(key) {
		return ""
	}
	return "web:" + tokenKey(key)
}

func webSameOrigin(r *http.Request) bool {
	if _, ok := r.Header["Origin"]; ok {
		return sameOrigin(r)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (g *Guard) webUnauthenticated(w http.ResponseWriter, r *http.Request, why error, browser string) {
	signIn := WebSignIn{Modes: g.auth.modes(), CSRFToken: browser}
	if g.auth.OIDC() {
		signIn.LoginURL = loginPath
		switch {
		case errors.Is(why, errRevoked):
			signIn.LoginURL += "?reason=revoked"
		case errors.Is(why, errExpired):
			signIn.LoginURL += "?reason=expired"
		}
		if !errors.Is(why, errAnonymous) {
			expireCookie(w, sessionCookie, g.secure(r))
		}
	}
	APIErrorWith(w, http.StatusUnauthorized, "unauthenticated", "Sign in first.", map[string]any{"sign_in": signIn})
}

func (s *Server) registerWebSession() {
	s.route(accessDefault, http.MethodGet, WebSessionPath, func(w http.ResponseWriter, r *http.Request) {
		who, _ := IdentityOf(r.Context())
		out := WebSession{AuthMode: who.Source, Identity: who, CSRFToken: CSRFToken(r.Context())}
		if who.Source == SourceOIDC {
			out.SignOut = &WebSignOut{Method: http.MethodPost, URL: WebLogoutPath}
		}
		JSON(w, http.StatusOK, out)
	})
	if !s.auth.OIDC() {
		return
	}
	s.route(accessDefault, http.MethodPost, WebLogoutPath, func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.account.Logout(r.Context(), authapp.LogoutInput{Token: cookieValue(r, sessionCookie, s.secure(r))}); err != nil {
			log.Error("sign-out failed", logx.Err, err.Error())
			APIError(w, http.StatusInternalServerError, "internal", "Internal error.")
			return
		}
		expireCookie(w, sessionCookie, s.secure(r))
		w.WriteHeader(http.StatusNoContent)
	})
}
