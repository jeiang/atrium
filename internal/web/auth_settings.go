package web

import (
	"net/http"
)

const authSettingsPath = "/settings/auth"

type authSettingsView struct {
	Active   bool
	Provider string
	Issuer   string
	ClientID string
	Redirect string
	Reason   string
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	data := authSettingsView{}
	cfg, err := s.oidc.Config(r.Context())
	if err != nil {
		data.Reason = err.Error()
	} else {
		data.Active = true
		data.Provider = cfg.Preset.Display
		if data.Provider == "" {
			data.Provider = cfg.Preset.Name
		}
		data.Issuer = cfg.Issuer
		data.ClientID = cfg.ClientID
		data.Redirect = cfg.RedirectURI
	}
	f, err := s.render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	if err := s.render.Page(w, r, http.StatusOK, "settings_auth.gohtml", f); err != nil {
		panic(err)
	}
}
