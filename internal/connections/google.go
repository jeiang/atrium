package connections

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const googlePath = path + "/google"

const (
	GoogleID       ID   = "google/default"
	KindGoogle     Kind = "google"
	GoogleCalendar      = "calendar"
	GoogleGmail         = "gmail"
)

const (
	googleCalendarScope = "https://www.googleapis.com/auth/calendar.events"
	googleGmailScope    = "https://mail.google.com/"
	googleStateTTL      = 10 * time.Minute
	googleExpiryWindow  = 14 * 24 * time.Hour
)

type googleGrant struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Scope  string `json:"scope"`
	Slot   string `json:"slot"`
	Shared string `json:"shared"`
}

var googleGrants = []googleGrant{
	{Name: GoogleCalendar, Label: "Calendar", Scope: googleCalendarScope, Slot: "oauth/calendar/refresh"},
	{Name: GoogleGmail, Label: "Gmail", Scope: googleGmailScope, Slot: "oauth/gmail/refresh"},
}

func googleGrantFor(name string) (googleGrant, bool) {
	for _, g := range googleGrants {
		if g.Name == name {
			return g, true
		}
	}
	return googleGrant{}, false
}

func init() {
	Register(Definition{Kind: KindGoogle, Validate: validateGoogle, Test: testGoogle})
}

func validateGoogle(c Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("google config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "client_id":
		case "auth_url", "token_url", "api_url":
			u, err := url.Parse(strings.TrimSpace(value))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("google %s must be an http or https URL", key)
			}
		default:
			return fmt.Errorf("google config has no %s field", key)
		}
	}
	for _, cap := range c.Capabilities {
		if _, ok := googleGrantFor(cap.Name); !ok {
			return fmt.Errorf("google has no %s capability", cap.Name)
		}
	}
	return nil
}

func googleConfig(lease *Lease) (clientID, authURL, tokenURL, apiURL string, err error) {
	var cfg map[string]string
	if err := json.Unmarshal(lease.Config(), &cfg); err != nil {
		return "", "", "", "", fmt.Errorf("connections: %s config: %w", lease.ID(), err)
	}
	clientID = strings.TrimSpace(cfg["client_id"])
	authURL = strings.TrimSpace(cfg["auth_url"])
	if authURL == "" {
		authURL = config.GoogleAuthURL()
	}
	tokenURL = strings.TrimSpace(cfg["token_url"])
	if tokenURL == "" {
		tokenURL = config.GoogleTokenURL()
	}
	apiURL = strings.TrimSpace(cfg["api_url"])
	if apiURL == "" {
		apiURL = config.GoogleAPIURL()
	}
	return clientID, authURL, tokenURL, apiURL, nil
}

type googleAccess struct {
	token  string
	expiry time.Time
}

type googleTokens struct {
	mu    sync.Mutex
	caps  map[string]*googleAccess
	waits map[string]chan struct{}
	errs  map[string]error
}

func (s *Store) googleTokens(id ID, revision int64) *googleTokens {
	sh := s.sharedFor(id, revision)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.google == nil {
		sh.google = &googleTokens{caps: map[string]*googleAccess{}, waits: map[string]chan struct{}{}, errs: map[string]error{}}
	}
	return sh.google
}

func (l *Lease) googleTokens() *googleTokens {
	return l.store.googleTokens(l.id, l.revision)
}

func (l *Lease) googleToken(ctx context.Context, capability string) (string, error) {
	grant, ok := googleGrantFor(capability)
	if !ok {
		return "", fmt.Errorf("google has no %s capability", capability)
	}
	tokens := l.googleTokens()
	tokens.mu.Lock()
	if access, ok := tokens.caps[capability]; ok && access.token != "" && time.Now().Before(access.expiry.Add(-time.Minute)) {
		token := access.token
		tokens.mu.Unlock()
		return token, nil
	}
	if waiting := tokens.waits[capability]; waiting != nil {
		tokens.mu.Unlock()
		select {
		case <-waiting:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		tokens.mu.Lock()
		access, err := tokens.caps[capability], tokens.errs[capability]
		tokens.mu.Unlock()
		if err != nil {
			return "", err
		}
		if access == nil {
			return "", errors.New("google: no access token")
		}
		return access.token, nil
	}
	waiting := make(chan struct{})
	tokens.waits[capability] = waiting
	tokens.mu.Unlock()

	access, err := l.refreshGoogle(ctx, grant)

	tokens.mu.Lock()
	delete(tokens.waits, capability)
	tokens.errs[capability] = err
	if err == nil {
		tokens.caps[capability] = access
	}
	close(waiting)
	tokens.mu.Unlock()
	if err != nil {
		l.observeGoogle(ctx, capability, err)
		return "", err
	}
	if access.expiry.Sub(time.Now()) < googleExpiryWindow {
		_ = l.Observe(ctx, Result{Capability: capability, State: Expiring,
			Reason: "access token expires " + access.expiry.Format(time.DateOnly), ExpiresAt: &access.expiry})
	}
	return access.token, nil
}

func (l *Lease) observeGoogle(ctx context.Context, capability string, err error) {
	state := Failing
	var rejected *googleRejected
	if errors.As(err, &rejected) {
		state = NeedsReauth
	}
	_ = l.Observe(ctx, Result{Capability: capability, State: state, Reason: err.Error()})
}

type googleRejected struct{ reason string }

func (e *googleRejected) Error() string { return e.reason }

func (l *Lease) refreshGoogle(ctx context.Context, grant googleGrant) (*googleAccess, error) {
	clientID, _, tokenURL, _, err := googleConfig(l)
	if err != nil {
		return nil, err
	}
	if clientID == "" {
		return nil, errors.New("google client ID is not set")
	}
	secret, err := l.Credential("oauth/client_secret")
	if err != nil {
		return nil, err
	}
	refresh, err := l.Credential(grant.Slot)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"refresh_token": {refresh},
		"scope":         {grant.Scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client, err := l.Transport()
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var reply struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
		Error       string  `json:"error"`
		Description string  `json:"error_description"`
	}
	_ = json.Unmarshal(body, &reply)
	if resp.StatusCode != http.StatusOK || reply.AccessToken == "" {
		reason := strings.TrimSpace(string(body))
		if reply.Error != "" {
			reason = reply.Error + ": " + reply.Description
		}
		message := fmt.Sprintf("token endpoint answered %d: %s", resp.StatusCode, reason)
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			return nil, &googleRejected{reason: message}
		}
		return nil, errors.New(message)
	}
	expiry := time.Now().Add(time.Hour)
	if reply.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	}
	return &googleAccess{token: reply.AccessToken, expiry: expiry}, nil
}

func (l *Lease) googleExpiry(capability string) *time.Time {
	tokens := l.googleTokens()
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	access, ok := tokens.caps[capability]
	if !ok || access.expiry.IsZero() {
		return nil
	}
	expiry := access.expiry
	return &expiry
}

func testGoogle(ctx context.Context, lease *Lease, caps []Capability) ([]Result, error) {
	if len(caps) == 0 {
		return []Result{{Capability: GoogleCalendar, State: Unconfigured, Reason: "no Google capability is enabled"}}, nil
	}
	results := make([]Result, 0, len(caps))
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		grant, ok := googleGrantFor(cap.Name)
		if !ok {
			results = append(results, Result{Capability: cap.Name, State: Failing, Reason: "unknown Google capability"})
			continue
		}
		token, err := lease.Token(ctx, grant.Name)
		if err != nil {
			state := Failing
			var rejected *googleRejected
			if errors.As(err, &rejected) {
				state = NeedsReauth
			}
			results = append(results, Result{Capability: grant.Name, State: state, Reason: err.Error()})
			continue
		}
		switch grant.Name {
		case GoogleCalendar:
			results = append(results, testGoogleCalendar(ctx, lease, token))
		case GoogleGmail:
			results = append(results, Result{Capability: grant.Name, State: OK, Reason: "refresh grant works", ExpiresAt: lease.googleExpiry(grant.Name)})
		}
	}
	if len(results) == 0 {
		return []Result{{Capability: GoogleCalendar, State: Unconfigured, Reason: "no Google capability is enabled"}}, nil
	}
	return results, nil
}

func testGoogleCalendar(ctx context.Context, lease *Lease, token string) Result {
	_, _, _, apiURL, err := googleConfig(lease)
	if err != nil {
		return Result{Capability: GoogleCalendar, State: Failing, Reason: err.Error()}
	}
	client, err := lease.Transport()
	if err != nil {
		return Result{Capability: GoogleCalendar, State: Failing, Reason: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(apiURL, "/")+"/calendars/primary/events?maxResults=1&singleEvents=true", nil)
	if err != nil {
		return Result{Capability: GoogleCalendar, State: Failing, Reason: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Result{Capability: GoogleCalendar, State: Failing, Reason: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
		data, _ := json.Marshal(map[string]any{"max_results": 1})
		return Result{Capability: GoogleCalendar, State: OK, Reason: "calendar read works", Data: data, ExpiresAt: lease.googleExpiry(GoogleCalendar)}
	case resp.StatusCode == http.StatusUnauthorized:
		return Result{Capability: GoogleCalendar, State: NeedsReauth, Reason: "Google refused the access token"}
	case resp.StatusCode == http.StatusForbidden:
		return Result{Capability: GoogleCalendar, State: Failing, Reason: "Google refused the calendar scope"}
	default:
		return Result{Capability: GoogleCalendar, State: Failing, Reason: fmt.Sprintf("calendar endpoint answered %d", resp.StatusCode)}
	}
}

// EnsureGoogle materializes the single Google connection and its two grants.
func (s *Store) EnsureGoogle(ctx context.Context) error {
	caps := make([]Capability, 0, len(googleGrants))
	for _, g := range googleGrants {
		caps = append(caps, Capability{Name: g.Name, Enabled: true, Config: json.RawMessage("{}")})
	}
	return s.Ensure(ctx, GoogleID, KindGoogle, "Google", caps)
}

type googlePage struct {
	ClientID        string         `json:"client_id"`
	ClientSecretSet bool           `json:"client_secret_set"`
	Redirect        string         `json:"redirect"`
	Capabilities    []googleGrant  `json:"capabilities"`
	Selected        string         `json:"selected"`
	Health          string         `json:"health"`
	Reason          string         `json:"reason"`
	Error           string         `json:"error"`
	Paste           string         `json:"paste"`
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
	view, err := s.Get(ctx, GoogleID)
	if err != nil {
		panic(err)
	}
	var cfg map[string]string
	_ = json.Unmarshal(view.Config, &cfg)
	page := googlePage{
		ClientID: cfg["client_id"], Redirect: googleRedirect(r), Capabilities: googleGrants,
		Selected: GoogleCalendar, Health: string(view.State), Reason: view.Reason,
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
	cand := Candidate{}
	if clientID != "" {
		raw, err := json.Marshal(map[string]string{"client_id": clientID})
		if err != nil {
			panic(err)
		}
		cand.Config = raw
	}
	if clientSecret != "" {
		cand.Credentials = append(cand.Credentials, CredentialInput{Slot: "oauth/client_secret", Value: clientSecret})
	}
	if _, err := s.Save(ctx, GoogleID, cand); err != nil {
		page := h.googlePage(r)
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page)
		return
	}
	http.Redirect(w, r, googlePath+"?saved=1", http.StatusSeeOther)
}

func (h handlers) googleStart(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	grant, ok := googleGrantFor(web.FormString(form, "capability", GoogleCalendar))
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

func (h handlers) startGoogle(ctx context.Context, grant googleGrant, redirect string) (string, error) {
	state, verifier := googleToken(24), googleToken(64)
	if _, err := h.d.DB.Write.ExecContext(ctx, "INSERT INTO oauth_states (state, verifier, capability, redirect_uri, created_ts) VALUES (?, ?, ?, ?, ?)",
		state, verifier, grant.Name, redirect, float64(time.Now().UnixNano())/1e9); err != nil {
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
	lease, err := s.Open(ctx, GoogleID)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	clientID, authURL, tokenURL, _, err := googleConfig(lease)
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

func (h handlers) completeGoogle(ctx context.Context, s *Store, state, code, authErr string) (googleGrant, error) {
	var verifier, capabilityName, redirect string
	var created float64
	err := h.d.DB.Read.QueryRowContext(ctx, "SELECT verifier, capability, redirect_uri, created_ts FROM oauth_states WHERE state = ?", state).
		Scan(&verifier, &capabilityName, &redirect, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return googleGrant{}, errors.New("this consent link is unknown or already used; start again")
	}
	if err != nil {
		return googleGrant{}, err
	}
	_, _ = h.d.DB.Write.ExecContext(ctx, "DELETE FROM oauth_states WHERE state = ?", state)
	if authErr != "" {
		return googleGrant{}, errors.New("Google returned an error: " + authErr)
	}
	if time.Since(time.Unix(int64(created), 0)) > googleStateTTL {
		return googleGrant{}, errors.New("the consent link expired; start again")
	}
	grant, ok := googleGrantFor(capabilityName)
	if !ok {
		return googleGrant{}, errors.New("the consent link names an unknown capability")
	}
	conf, err := h.googleOAuth(ctx, redirect, grant.Scope)
	if err != nil {
		return googleGrant{}, err
	}
	lease, err := s.Open(ctx, GoogleID)
	if err != nil {
		return googleGrant{}, err
	}
	client, err := lease.Transport()
	lease.Close()
	if err != nil {
		return googleGrant{}, err
	}
	exchange := context.WithValue(ctx, oauth2.HTTPClient, client)
	token, err := conf.Exchange(exchange, code, oauth2.VerifierOption(verifier))
	if err != nil || token.RefreshToken == "" {
		if err == nil {
			err = errors.New("Google returned no refresh token; revoke the app's access and retry")
		}
		return googleGrant{}, err
	}
	if _, err := s.Save(ctx, GoogleID, Candidate{Credentials: []CredentialInput{{Slot: grant.Slot, Value: token.RefreshToken}}}); err != nil {
		return googleGrant{}, err
	}
	return grant, nil
}

func (h handlers) googleTest(w http.ResponseWriter, r *http.Request) {
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		panic(err)
	}
	view, err := s.Test(ctx, GoogleID)
	if err != nil {
		page := h.googlePage(r)
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusBadRequest, &tpl.Toast{Category: "error", Title: "Not tested", Description: err.Error()}, page)
		return
	}
	if view.State != OK && view.State != Expiring {
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

// GoogleGrantSlot returns the credential slot holding a capability's refresh
// grant.
func GoogleGrantSlot(capability string) (string, bool) {
	grant, ok := googleGrantFor(capability)
	return grant.Slot, ok
}

// StoreGoogleGrant saves a refresh grant obtained by the local consent flow.
func StoreGoogleGrant(ctx context.Context, d *db.DB, capability, refresh string) error {
	slot, ok := GoogleGrantSlot(capability)
	if !ok {
		return fmt.Errorf("google has no %s capability", capability)
	}
	s, err := New(d, config.From(ctx).Host(), DefaultRegistry())
	if err != nil {
		return err
	}
	if err := s.EnsureGoogle(ctx); err != nil {
		return err
	}
	_, err = s.Save(ctx, GoogleID, Candidate{Credentials: []CredentialInput{{Slot: slot, Value: refresh}}})
	return err
}
