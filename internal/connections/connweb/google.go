package connweb

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type googlePage struct {
	ClientID        string                    `json:"client_id"`
	ClientSecretSet bool                      `json:"client_secret_set"`
	Redirect        string                    `json:"redirect"`
	Capabilities    []connections.GoogleGrant `json:"capabilities"`
	Selected        string                    `json:"selected"`
	Health          string                    `json:"health"`
	Reason          string                    `json:"reason"`
	Error           string                    `json:"error"`
	Paste           string                    `json:"paste"`
}

func googleRedirect(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && strings.HasPrefix(r.Host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + googlePath + "/callback"
}

func (h handlers) googlePage(r *http.Request) googlePage {
	ctx := r.Context()
	s, err := h.store(ctx)
	if err != nil {
		panic(err)
	}
	if err := s.EnsureGoogle(ctx); err != nil {
		panic(err)
	}
	view, err := s.Get(ctx, connections.GoogleID)
	if err != nil {
		panic(err)
	}
	var cfg map[string]string
	_ = json.Unmarshal(view.Config, &cfg)
	page := googlePage{
		ClientID: cfg["client_id"], Redirect: googleRedirect(r), Capabilities: connections.GoogleGrants(),
		Selected: connections.GoogleCalendar, Health: string(view.State), Reason: view.Reason,
	}
	for _, cred := range view.Credentials {
		if cred.Slot == "oauth/client_secret" && cred.Present {
			page.ClientSecretSet = true
		}
	}
	return page
}

func (h handlers) googleRender(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data googlePage) {
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "google.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) google(w http.ResponseWriter, r *http.Request) {
	h.googleRender(w, r, http.StatusOK, nil, h.googlePage(r))
}

func (h handlers) googleSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	clientID := web.FormString(form, "client_id", "")
	clientSecret := web.FormString(form, "client_secret", "")
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		h.googleRender(w, r, http.StatusInternalServerError, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, h.googlePage(r))
		return
	}
	if err := s.EnsureGoogle(ctx); err != nil {
		h.googleRender(w, r, http.StatusInternalServerError, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, h.googlePage(r))
		return
	}
	cand := connections.Candidate{}
	if clientID != "" {
		raw, err := json.Marshal(map[string]string{"client_id": clientID})
		if err != nil {
			panic(err)
		}
		cand.Config = raw
	}
	if clientSecret != "" {
		cand.Credentials = append(cand.Credentials, connections.CredentialInput{Slot: "oauth/client_secret", Value: clientSecret})
	}
	if _, err := s.Save(ctx, connections.GoogleID, cand); err != nil {
		page := h.googlePage(r)
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page)
		return
	}
	http.Redirect(w, r, googlePath+"?saved=1", http.StatusSeeOther)
}

func (h handlers) googleStart(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	grant, ok := connections.GoogleGrantFor(web.FormString(form, "capability", connections.GoogleCalendar))
	if !ok {
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not started", Description: "unknown capability"}, h.googlePage(r))
		return
	}
	page := h.googlePage(r)
	if page.ClientID == "" || !page.ClientSecretSet {
		page.Error = "Save the client ID and client secret first."
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not started", Description: page.Error}, page)
		return
	}
	authURL, err := h.startGoogle(r.Context(), grant, page.Redirect)
	if err != nil {
		panic(err)
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (h handlers) startGoogle(ctx context.Context, grant connections.GoogleGrant, redirect string) (string, error) {
	state, verifier := googleToken(24), googleToken(64)
	s, err := h.store(ctx)
	if err != nil {
		return "", err
	}
	if err := s.SaveOAuthState(ctx, state, verifier, grant.Name, redirect); err != nil {
		return "", err
	}
	conf, err := h.googleOAuth(ctx, redirect, grant.Scope)
	if err != nil {
		return "", err
	}
	return conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "consent")), nil
}

func (h handlers) googleOAuth(ctx context.Context, redirect string, scopes ...string) (*oauth2.Config, error) {
	s, err := h.store(ctx)
	if err != nil {
		return nil, err
	}
	lease, err := s.Open(ctx, connections.GoogleID)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	clientID, authURL, tokenURL, _, err := connections.GoogleConfig(lease)
	if err != nil {
		return nil, err
	}
	secret, err := lease.Credential("oauth/client_secret")
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID: clientID, ClientSecret: secret, RedirectURL: redirect, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}, nil
}

func (h handlers) googleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	h.googleComplete(w, r, query.Get("state"), query.Get("code"), query.Get("error"))
}

func (h handlers) googlePaste(w http.ResponseWriter, r *http.Request) {
	pasted := web.FormString(web.FormValues(r), "paste", "")
	parsed, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || parsed.Query().Get("code") == "" {
		page := h.googlePage(r)
		page.Error = "Paste the full URL you were redirected to, including the code."
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not connected", Description: page.Error}, page)
		return
	}
	h.googleComplete(w, r, parsed.Query().Get("state"), parsed.Query().Get("code"), parsed.Query().Get("error"))
}

func (h handlers) googleComplete(w http.ResponseWriter, r *http.Request, state, code, authErr string) {
	page := h.googlePage(r)
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		panic(err)
	}
	grant, err := h.completeGoogle(ctx, s, state, code, authErr)
	if err != nil {
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not connected", Description: page.Error}, page)
		return
	}
	http.Redirect(w, r, googlePath+"?connected="+grant.Name, http.StatusSeeOther)
}

func (h handlers) completeGoogle(ctx context.Context, s *connections.Store, state, code, authErr string) (connections.GoogleGrant, error) {
	pending, err := s.ConsumeOAuthState(ctx, state)
	if err != nil {
		return connections.GoogleGrant{}, err
	}
	if authErr != "" {
		return connections.GoogleGrant{}, errors.New("Google returned an error: " + authErr)
	}
	if time.Since(pending.Created) > connections.GoogleStateTTL {
		return connections.GoogleGrant{}, errors.New("the consent link expired; start again")
	}
	grant, ok := connections.GoogleGrantFor(pending.Capability)
	if !ok {
		return connections.GoogleGrant{}, errors.New("the consent link names an unknown capability")
	}
	conf, err := h.googleOAuth(ctx, pending.Redirect, grant.Scope)
	if err != nil {
		return connections.GoogleGrant{}, err
	}
	lease, err := s.Open(ctx, connections.GoogleID)
	if err != nil {
		return connections.GoogleGrant{}, err
	}
	client, err := lease.Transport()
	lease.Close()
	if err != nil {
		return connections.GoogleGrant{}, err
	}
	exchange := context.WithValue(ctx, oauth2.HTTPClient, client)
	token, err := conf.Exchange(exchange, code, oauth2.VerifierOption(pending.Verifier))
	if err != nil || token.RefreshToken == "" {
		if err == nil {
			err = errors.New("Google returned no refresh token; revoke the app's access and retry") //nolint:staticcheck // the CLI contract keeps this wording
		}
		return connections.GoogleGrant{}, err
	}
	if _, err := s.Save(ctx, connections.GoogleID, connections.Candidate{Credentials: []connections.CredentialInput{{Slot: grant.Slot, Value: token.RefreshToken}}}); err != nil {
		return connections.GoogleGrant{}, err
	}
	return grant, nil
}

func (h handlers) googleTest(w http.ResponseWriter, r *http.Request) {
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		panic(err)
	}
	view, err := s.Test(ctx, connections.GoogleID)
	if err != nil {
		page := h.googlePage(r)
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusBadRequest, &tpl.Toast{Category: "error", Title: "Not tested", Description: err.Error()}, page)
		return
	}
	if view.State != connections.OK && view.State != connections.Expiring {
		page := h.googlePage(r)
		page.Error = view.Reason
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not verified", Description: view.Reason}, page)
		return
	}
	h.googleRender(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connected", Description: "The refresh token works."}, h.googlePage(r))
}

func googleToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
