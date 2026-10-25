package web

import (
	"errors"
	"net/http"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
)

func (s *Server) registerAuthRoutes(env config.Env) {
	publicSecure := func() bool { return len(config.PublicURL(env)) >= 8 && config.PublicURL(env)[:8] == "https://" }
	secure := func(r *http.Request) bool { return hasSecureTransport(r, publicSecure()) }
	s.mux.Get(LoginPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if _, ok := IdentityOf(r.Context()); ok {
			http.Redirect(w, r, oidc.SafeReturn(r.URL.Query().Get("return")), http.StatusSeeOther)
			return
		}
		login, err := s.oidc.Begin(r.Context(), r.URL.Query().Get("return"))
		if err != nil {
			s.signInFailed(w, r, err)
			return
		}
		setCookie(w, bindingCookie, login.Binding, 600, secure(r))
		http.Redirect(w, r, login.URL, http.StatusSeeOther)
	})
	s.mux.Get(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		expireCookie(w, bindingCookie, secure(r))
		q := r.URL.Query()
		if q.Get("error") != "" {
			log.Warn("provider refused the sign-in", "error", q.Get("error"))
			s.signInFailed(w, r, oidc.ErrDenied)
			return
		}
		grant, err := s.oidc.Complete(r.Context(), q.Get("state"), q.Get("code"), cookieValue(r, bindingCookie))
		if err != nil {
			s.signInFailed(w, r, err)
			return
		}
		setCookie(w, sessionCookie, grant.Token, int(grant.MaxAge.Seconds()), secure(r))
		http.Redirect(w, r, grant.ReturnTo, http.StatusSeeOther)
	})
	s.mux.Post(LogoutPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if err := s.oidc.End(r.Context(), cookieValue(r, sessionCookie)); err != nil {
			log.Error("sign-out failed", logx.Err, err.Error())
			s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		expireCookie(w, sessionCookie, secure(r))
		http.Redirect(w, r, SignedOutPath, http.StatusSeeOther)
	})
	s.mux.Get(SignedOutPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		s.authPage(w, r, http.StatusOK, authSignedOut)
	})
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, err error) {
	var unconfigured *oidc.ConfigError
	switch {
	case errors.Is(err, oidc.ErrDenied):
		s.authPage(w, r, http.StatusForbidden, authDenied)
	case errors.Is(err, oidc.ErrLogin):
		s.authPage(w, r, http.StatusBadRequest, authExpired)
	default:
		if errors.As(err, &unconfigured) || errors.Is(err, oidc.ErrNoPKCE) || errors.Is(err, oidc.ErrProvider) || errors.Is(err, oidc.ErrRejected) || errors.Is(err, oidc.ErrToken) {
			log.Error("sign-in failed", logx.Err, err.Error())
			s.authPage(w, r, http.StatusBadGateway, authUnavailable)
			return
		}
		log.Error("sign-in failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
	}
}
