package web

import "net/http"

const (
	authSignedOut   = "signed-out"
	authDenied      = "denied"
	authExpired     = "expired"
	authUnavailable = "unavailable"
)

func (s *Server) authPage(w http.ResponseWriter, r *http.Request, status int, page string) {
	deny(w, status, page)
}
