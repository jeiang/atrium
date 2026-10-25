package web

import (
	"errors"
	"net/http"

	"github.com/jeiang/atrium/internal/auth/loginlink"
	"github.com/jeiang/atrium/internal/logx"
)

type linkView struct {
	Message string `json:"message"`
}

func (s *Server) linkPage(w http.ResponseWriter, r *http.Request, status int, message string) {
	noStore(w)
	f, err := s.render.Frame(r, "login", linkView{Message: message})
	if err == nil {
		err = s.render.Page(w, r, status, "auth_link.gohtml", f)
	}
	if err != nil {
		log.Error("render login link failed", logx.Err, err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}

func (s *Server) registerLinkRoutes() {
	s.mux.Get(loginlink.Path, func(w http.ResponseWriter, r *http.Request) {
		s.linkPage(w, r, http.StatusOK, "")
	})
	s.mux.Post(loginlink.Path, func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		err := s.links.Redeem(r.Context(), FormString(FormValues(r), "token", ""))
		switch {
		case errors.Is(err, loginlink.ErrUsed):
			s.linkPage(w, r, http.StatusGone, "This link was already used.")
			return
		case errors.Is(err, loginlink.ErrExpired):
			s.linkPage(w, r, http.StatusGone, "This link expired.")
			return
		case errors.Is(err, loginlink.ErrInvalid):
			s.linkPage(w, r, http.StatusBadRequest, "This link is not valid.")
			return
		case err != nil:
			log.Error("login link redemption failed", logx.Err, err.Error())
			s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		grant, err := s.oidc.IssueLinkSession(r.Context())
		if err != nil {
			log.Error("login link session failed", logx.Err, err.Error())
			s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		setCookie(w, sessionCookie, grant.Token, int(grant.MaxAge.Seconds()), s.secure(r))
		target := "/chat"
		if s.oidc.Check(r.Context()) != nil {
			target = WizardPath
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}
