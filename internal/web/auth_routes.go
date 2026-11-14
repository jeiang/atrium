package web

import (
	"errors"
	"net/http"
	"strings"

	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/urlx"
)

type loginState struct {
	status  int
	tone    string
	message string
	start   bool
}

var loginStates = map[string]loginState{
	"":             {http.StatusOK, "", "", true},
	"signed_out":   {http.StatusOK, "neutral", "You are signed out.", true},
	"expired":      {http.StatusOK, "neutral", "Your session expired.", true},
	"revoked":      {http.StatusOK, "destructive", "Your access was removed.", true},
	"denied":       {http.StatusForbidden, "destructive", "This account does not have access.", true},
	"refused":      {http.StatusBadRequest, "destructive", "The provider rejected the sign-in.", true},
	"timeout":      {http.StatusBadRequest, "destructive", "Sign-in timed out.", true},
	"unreachable":  {http.StatusBadGateway, "destructive", "The provider did not respond.", true},
	"unconfigured": {http.StatusOK, "neutral", "Sign-in is not set up.", false},
}

type loginView struct {
	Next    string `json:"next"`
	Button  string `json:"button"`
	Reason  string `json:"reason"`
	Tone    string `json:"tone"`
	Message string `json:"message"`
	Start   bool   `json:"start"`
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request, reason, next string) {
	state, ok := loginStates[reason]
	if !ok {
		reason, state = "", loginStates[""]
	}
	button := "Sign in"
	cfg, err := s.account.Configuration(r.Context())
	if err != nil {
		log.Error("sign-in configuration failed", logx.Err, err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !cfg.Data.Active {
		reason, state = "unconfigured", loginStates["unconfigured"]
	} else {
		if cfg.Data.Display != "" {
			button = "Sign in with " + cfg.Data.Display
		}
		w.Header().Set("Content-Security-Policy", strings.Replace(csp, "form-action 'self'", "form-action 'self' "+strings.Join(cfg.Data.Origins, " "), 1))
	}
	view := loginView{Next: urlx.SafeReturn(next), Button: button, Reason: reason, Tone: state.tone, Message: state.message, Start: state.start}
	if s.render == nil {
		deny(w, state.status, state.message)
		return
	}
	f, err := s.render.Frame(r, "login", view)
	if err == nil {
		err = s.render.Page(w, r, state.status, "auth_login.gohtml", f)
	}
	if err != nil {
		log.Error("render login failed", logx.Err, err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}

func (s *Server) registerAuthRoutes() {
	secure := s.secure
	s.route(accessPublic, http.MethodGet, loginPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		q := r.URL.Query()
		if _, ok := IdentityOf(r.Context()); ok {
			http.Redirect(w, r, urlx.SafeReturn(q.Get("next")), http.StatusSeeOther)
			return
		}
		s.loginPage(w, r, q.Get("reason"), q.Get("next"))
	})
	s.route(accessPublic, http.MethodPost, loginPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		form := FormValues(r)
		next := form.Get("next")
		login, err := s.account.SigninStart(r.Context(), authapp.SigninStartInput{Next: next})
		if err != nil {
			s.signInFailed(w, r, err, next)
			return
		}
		setCookie(w, bindingCookie, login.Data.Binding, 600, secure(r))
		http.Redirect(w, r, login.Data.URL, http.StatusSeeOther)
	})
	s.route(accessPublic, http.MethodGet, callbackPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		q := r.URL.Query()
		expireCookie(w, bindingCookie, secure(r))
		if q.Get("error") != "" {
			log.Warn("provider refused the sign-in", "error", sanitizeLog(q.Get("error")))
			s.loginPage(w, r, "refused", "")
			return
		}
		grant, err := s.account.SigninComplete(r.Context(), authapp.SigninCompleteInput{State: q.Get("state"), Code: q.Get("code"), Binding: cookieValue(r, bindingCookie, secure(r))})
		if err != nil {
			s.signInFailed(w, r, err, "")
			return
		}
		setCookie(w, sessionCookie, grant.Data.Token, int(grant.Data.MaxAge.Seconds()), secure(r))
		http.Redirect(w, r, grant.Data.ReturnTo, http.StatusSeeOther)
	})
	s.route(accessDefault, http.MethodPost, logoutPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if _, err := s.account.Logout(r.Context(), authapp.LogoutInput{Token: cookieValue(r, sessionCookie, secure(r))}); err != nil {
			log.Error("sign-out failed", logx.Err, err.Error())
			s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		expireCookie(w, sessionCookie, secure(r))
		http.Redirect(w, r, loginPath+"?reason=signed_out", http.StatusSeeOther)
	})
}

func sanitizeLog(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return -1
		}
		return r
	}, s)
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, err error, next string) {
	var failed *authapp.SigninError
	if !errors.As(err, &failed) {
		log.Error("sign-in failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	log.Warn("sign-in failed", "reason", failed.Reason, logx.Err, failed.Cause.Error())
	s.loginPage(w, r, failed.Reason, next)
}
