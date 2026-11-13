package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	authapp "github.com/jeiang/atrium/internal/auth/application"
	"github.com/jeiang/atrium/internal/logx"
)

const authSettingsPath = "/settings/auth"

type authSettingsView struct {
	Active     bool
	Configured bool
	Provider   string
	Issuer     string
	ClientID   string
	Redirect   string
	Reason     string
	Tokens     []tokenView
	Secret     string
	Error      string
	Notice     string
}

type tokenView struct {
	ID       int64
	Name     string
	Kind     string
	Subject  string
	Created  string
	Expires  string
	LastUsed string
	Revoked  string
	Active   bool
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	s.renderAuth(w, r, http.StatusOK, authSettingsView{})
}

func (s *Server) tokenIssue(w http.ResponseWriter, r *http.Request) {
	if s.account == nil {
		s.Error(w, r, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	form := FormValues(r)
	in := authapp.BearerIssueInput{Name: form.Get("name")}
	if raw := strings.TrimSpace(form.Get("expires_at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.renderAuth(w, r, http.StatusUnprocessableEntity, authSettingsView{Error: "The expiry must be an RFC 3339 timestamp."})
			return
		}
		in.ExpiresAt = &parsed
	}
	issued, err := s.account.BearerIssue(r.Context(), in)
	if err != nil {
		s.authFailed(w, r, err)
		return
	}
	s.renderAuth(w, r, http.StatusOK, authSettingsView{Secret: issued.Data.Secret, Notice: fmt.Sprintf("Token %d issued.", issued.Data.Token.ID)})
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.account == nil {
		s.Error(w, r, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	id, err := strconv.ParseInt(PathParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		s.Error(w, r, http.StatusNotFound, "Not Found")
		return
	}
	if _, err := s.account.BearerRevoke(r.Context(), authapp.BearerRevokeInput{ID: id}); err != nil {
		s.authFailed(w, r, err)
		return
	}
	s.renderAuth(w, r, http.StatusOK, authSettingsView{Notice: fmt.Sprintf("Token %d revoked.", id)})
}

func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, err error) {
	Fail(w, r, err, func(status int) {
		e := application.Classify(err)
		if status == http.StatusNotFound {
			s.Error(w, r, status, "Not Found")
			return
		}
		if status >= http.StatusInternalServerError {
			log.Error("token operation failed", logx.Err, err.Error())
		}
		s.renderAuth(w, r, status, authSettingsView{Error: Message(e)})
	})
}

func (s *Server) renderAuth(w http.ResponseWriter, r *http.Request, status int, view authSettingsView) {
	noStore(w)
	if s.account != nil {
		cfg, err := s.account.Configuration(r.Context())
		if err != nil {
			log.Error("sign-in configuration failed", logx.Err, err.Error())
		}
		c := cfg.Data
		view.Configured, view.Active, view.Provider, view.Issuer, view.ClientID, view.Redirect, view.Reason = c.Configured, c.Active, c.Provider, c.Issuer, c.ClientID, c.Redirect, c.Reason
		tokens, err := s.account.BearerList(r.Context())
		if err != nil {
			log.Error("token list failed", logx.Err, err.Error())
		} else {
			view.Tokens = tokenViews(tokens.Data)
		}
	}
	f, err := s.render.Frame(r, "settings", view)
	if err != nil {
		panic(err)
	}
	if err := s.render.Page(w, r, status, "settings_auth.gohtml", f); err != nil {
		panic(err)
	}
}

func tokenViews(tokens []authapp.Token) []tokenView {
	out := make([]tokenView, len(tokens))
	for i, tok := range tokens {
		owner := tok.Subject
		if tok.Kind == "local" {
			owner = "Operator"
		}
		out[i] = tokenView{
			ID: tok.ID, Name: tok.Name, Kind: tok.Kind, Subject: owner,
			Created: tokenStamp(&tok.CreatedAt), Expires: tokenStamp(tok.ExpiresAt),
			LastUsed: tokenStamp(tok.LastUsedAt), Revoked: tokenStamp(tok.RevokedAt),
			Active: tok.RevokedAt == nil,
		}
	}
	return out
}

func tokenStamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
