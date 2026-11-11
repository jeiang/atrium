package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const authSettingsPath = "/settings/auth"

type authSettingsView struct {
	Active   bool
	Provider string
	Issuer   string
	ClientID string
	Redirect string
	Reason   string
	Tokens   []tokenView
	Secret   string
	Error    string
	Notice   string
}

type tokenView struct {
	ID       int64
	Name     string
	Kind     string
	Owner    string
	Created  string
	Expires  string
	LastUsed string
	Revoked  string
	Active   bool
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	s.renderAuth(w, r, authSettingsView{})
}

func (s *Server) tokenIssue(w http.ResponseWriter, r *http.Request) {
	if s.bearers == nil {
		s.Error(w, r, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	form := FormValues(r)
	var at *time.Time
	if raw := strings.TrimSpace(form.Get("expires_at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.renderAuth(w, r, authSettingsView{Error: "The expiry must be an RFC 3339 timestamp."})
			return
		}
		at = &parsed
	}
	ctx := audit.With(r.Context(), "operator", audit.UI)
	issued, err := s.bearers.Issue(ctx, form.Get("name"), at)
	switch {
	case errors.Is(err, bearer.ErrName):
		s.renderAuth(w, r, authSettingsView{Error: "A name of at most 100 characters is required."})
		return
	case errors.Is(err, bearer.ErrExpiry):
		s.renderAuth(w, r, authSettingsView{Error: "The expiry must be in the future."})
		return
	case err != nil:
		log.Error("token issue failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if err := s.recordToken(ctx, "auth_token.issue", issued.Token); err != nil {
		log.Error("token audit failed", logx.Err, err.Error())
	}
	s.renderAuth(w, r, authSettingsView{Secret: issued.Secret, Notice: fmt.Sprintf("Token %d issued.", issued.Token.ID)})
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.bearers == nil {
		s.Error(w, r, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	id, err := strconv.ParseInt(PathParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		s.Error(w, r, http.StatusNotFound, "Not Found")
		return
	}
	ctx := audit.With(r.Context(), "operator", audit.UI)
	tokens, err := s.bearers.List(ctx)
	if err != nil {
		log.Error("token list failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	var target bearer.Token
	for _, tok := range tokens {
		if tok.ID == id {
			target = tok
		}
	}
	switch err := s.bearers.Revoke(ctx, id); {
	case errors.Is(err, bearer.ErrNotFound):
		s.Error(w, r, http.StatusNotFound, "Not Found")
		return
	case err != nil:
		log.Error("token revoke failed", logx.Err, err.Error())
		s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if target.ID != 0 && target.RevokedAt == nil {
		if err := s.recordToken(ctx, "auth_token.revoke", target); err != nil {
			log.Error("token audit failed", logx.Err, err.Error())
		}
	}
	s.renderAuth(w, r, authSettingsView{Notice: fmt.Sprintf("Token %d revoked.", id)})
}

func (s *Server) recordToken(ctx context.Context, key string, tok bearer.Token) error {
	if s.db == nil {
		return nil
	}
	value := fmt.Sprintf("id=%d name=%s kind=%s", tok.ID, tok.Name, tok.Kind)
	return audit.Record(ctx, s.db.Write, audit.Entry{Key: key, New: &value})
}

func (s *Server) renderAuth(w http.ResponseWriter, r *http.Request, view authSettingsView) {
	noStore(w)
	if s.oidc != nil {
		cfg, err := s.oidc.Config(r.Context())
		if err != nil {
			view.Reason = err.Error()
		} else {
			view.Active = true
			view.Provider = cfg.Preset.Display
			if view.Provider == "" {
				view.Provider = cfg.Preset.Name
			}
			view.Issuer = cfg.Issuer
			view.ClientID = cfg.ClientID
			view.Redirect = cfg.RedirectURI
		}
	}
	if s.bearers != nil {
		tokens, err := s.bearers.List(r.Context())
		if err != nil {
			log.Error("token list failed", logx.Err, err.Error())
		} else {
			view.Tokens = tokenViews(tokens)
		}
	}
	f, err := s.render.Frame(r, "settings", view)
	if err != nil {
		panic(err)
	}
	if err := s.render.Page(w, r, http.StatusOK, "settings_auth.gohtml", f); err != nil {
		panic(err)
	}
}

func tokenViews(tokens []bearer.Token) []tokenView {
	out := make([]tokenView, len(tokens))
	for i, tok := range tokens {
		owner := tok.Subject
		if tok.Kind == "local" {
			owner = "Operator"
		}
		out[i] = tokenView{
			ID: tok.ID, Name: tok.Name, Kind: tok.Kind, Owner: owner,
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
