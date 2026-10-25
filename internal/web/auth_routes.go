package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
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
	if cfg, err := s.oidc.Config(r.Context()); err != nil {
		reason, state = "unconfigured", loginStates["unconfigured"]
	} else if cfg.Preset.Display != "" {
		button = "Sign in with " + cfg.Preset.Display
	}
	if cfg, err := s.oidc.Config(r.Context()); err == nil {
		w.Header().Set("Content-Security-Policy", strings.Replace(CSP, "form-action 'self'", "form-action 'self' "+strings.Join(s.oidc.Origins(cfg), " "), 1))
	}
	view := loginView{Next: oidc.SafeReturn(next), Button: button, Reason: reason, Tone: state.tone, Message: state.message, Start: state.start}
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
	s.mux.Get(LoginPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		q := r.URL.Query()
		if _, ok := IdentityOf(r.Context()); ok {
			http.Redirect(w, r, oidc.SafeReturn(q.Get("next")), http.StatusSeeOther)
			return
		}
		s.loginPage(w, r, q.Get("reason"), q.Get("next"))
	})
	s.mux.Post(LoginPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		form := FormValues(r)
		next := form.Get("next")
		login, err := s.oidc.Begin(r.Context(), next)
		if err != nil {
			s.signInFailed(w, r, err, next)
			return
		}
		setCookie(w, bindingCookie, login.Binding, 600, secure(r))
		http.Redirect(w, r, login.URL, http.StatusSeeOther)
	})
	s.registerLinkRoutes()
	s.registerWizardRoutes()
	s.mux.Get(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		q := r.URL.Query()
		if s.oidc.IsTestState(q.Get("state")) {
			s.testCallback(w, r)
			return
		}
		expireCookie(w, bindingCookie, secure(r))
		if q.Get("error") != "" {
			log.Warn("provider refused the sign-in", "error", sanitizeLog(q.Get("error")))
			s.loginPage(w, r, "refused", "")
			return
		}
		grant, err := s.oidc.Complete(r.Context(), q.Get("state"), q.Get("code"), cookieValue(r, bindingCookie, secure(r)))
		if err != nil {
			s.signInFailed(w, r, err, "")
			return
		}
		setCookie(w, sessionCookie, grant.Token, int(grant.MaxAge.Seconds()), secure(r))
		http.Redirect(w, r, grant.ReturnTo, http.StatusSeeOther)
	})
	s.mux.Post(LogoutPath, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if err := s.oidc.End(r.Context(), cookieValue(r, sessionCookie, secure(r))); err != nil {
			log.Error("sign-out failed", logx.Err, err.Error())
			s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		expireCookie(w, sessionCookie, secure(r))
		http.Redirect(w, r, LoginPath+"?reason=signed_out", http.StatusSeeOther)
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
	var unconfigured *oidc.ConfigError
	reason := ""
	switch {
	case errors.Is(err, oidc.ErrDenied):
		reason = "denied"
	case errors.Is(err, oidc.ErrLoginTimeout):
		reason = "timeout"
	case errors.Is(err, oidc.ErrLogin), errors.Is(err, oidc.ErrNoPKCE), errors.Is(err, oidc.ErrRejected), errors.Is(err, oidc.ErrToken):
		reason = "refused"
	case errors.Is(err, oidc.ErrProvider):
		reason = "unreachable"
	case errors.As(err, &unconfigured):
		reason = "unconfigured"
	default:
		log.Error("sign-in failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	log.Warn("sign-in failed", "reason", reason, logx.Err, err.Error())
	s.loginPage(w, r, reason, next)
}

func (s *Server) registerWizardRoutes() {
	s.mux.Get(WizardPath, s.wizardPage)
	s.mux.Get(WizardPath+"/preset", s.wizardPreset)
	s.mux.Post(testPath, s.wizardTest)
	s.mux.Get(testPath+"/status", s.wizardStatus)
	s.mux.Post(WizardPath+"/activate", s.wizardActivate)
}
