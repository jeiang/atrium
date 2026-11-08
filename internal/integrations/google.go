package integrations

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
	"time"

	"golang.org/x/oauth2"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	googlePath         = path + "/google"
	googleCallbackPath = googlePath + "/callback"
	googlePastePath    = googlePath + "/paste"
	googleTestPath     = googlePath + "/test"
	googleClientIDKey  = "google_client_id"
	googleStateTTL     = 10 * time.Minute
)

type googleCapability struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Scope  string `json:"scope"`
	Secret string `json:"secret"`
}

var googleCapabilities = []googleCapability{
	{"calendar", "Calendar", "https://www.googleapis.com/auth/calendar.events", secrets.GoogleCalendarRefreshToken},
	{"gmail", "Gmail", "https://mail.google.com/", secrets.GoogleGmailRefreshToken},
}

func capability(name string) (googleCapability, bool) {
	for _, c := range googleCapabilities {
		if c.Name == name {
			return c, true
		}
	}
	return googleCapability{}, false
}

type googlePage struct {
	ClientID        string             `json:"client_id"`
	ClientSecretSet bool               `json:"client_secret_set"`
	Redirect        string             `json:"redirect"`
	Capabilities    []googleCapability `json:"capabilities"`
	Selected        string             `json:"selected"`
	Health          string             `json:"health"`
	Reason          string             `json:"reason"`
	Error           string             `json:"error"`
	Paste           string             `json:"paste"`
}

func (h handlers) googleRedirect(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && strings.HasPrefix(r.Host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + googleCallbackPath
}

func (h handlers) googlePage(r *http.Request) googlePage {
	ctx := r.Context()
	found, err := settings.From(ctx).Resolve(ctx, h.d.DB.Read, googleClientIDKey)
	if err != nil {
		panic(err)
	}
	store := secrets.NewIn(settings.From(ctx), h.d.DB, h.d.DB.Read, nil)
	status, err := store.Status(ctx, secrets.GoogleOAuthClientSecret)
	if err != nil {
		panic(err)
	}
	page := googlePage{
		ClientID: strings.TrimSpace(found.Value), ClientSecretSet: status != settings.Unset,
		Redirect: h.googleRedirect(r), Capabilities: googleCapabilities, Selected: "calendar",
	}
	var reason string
	err = h.d.DB.Read.QueryRowContext(ctx, "SELECT state, reason FROM integration_health WHERE name = 'google'").Scan(&page.Health, &reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		panic(err)
	}
	page.Reason = reason
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
	ctx := h.ctx(r)
	err := h.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if clientID != "" {
			if err := settings.From(ctx).Set(ctx, tx, googleClientIDKey, clientID); err != nil {
				return err
			}
		}
		if clientSecret != "" {
			if err := secrets.NewIn(settings.From(ctx), h.d.DB, tx, h.d.DB).Set(ctx, secrets.GoogleOAuthClientSecret, clientSecret, "ui"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		page := h.googlePage(r)
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page)
		return
	}
	w.Header().Set("Location", googlePath+"?saved=1")
	w.WriteHeader(http.StatusSeeOther)
}

func (h handlers) googleStart(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	cap, ok := capability(web.FormString(form, "capability", "calendar"))
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
	authURL, err := startGoogle(h.ctx(r), h.d.DB, page.ClientID, cap, page.Redirect)
	if err != nil {
		panic(err)
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func startGoogle(ctx context.Context, d *db.DB, clientID string, cap googleCapability, redirect string) (string, error) {
	state, verifier := token(24), token(64)
	if _, err := d.Write.ExecContext(ctx, "INSERT INTO oauth_states (state, verifier, capability, redirect_uri, created_ts) VALUES (?, ?, ?, ?, ?)",
		state, verifier, cap.Name, redirect, stamp()); err != nil {
		return "", err
	}
	conf := oauthConfig(ctx, d, clientID, redirect, cap.Scope)
	return conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "consent")), nil
}

func oauthConfig(ctx context.Context, d *db.DB, clientID, redirect string, scopes ...string) *oauth2.Config {
	secret, _ := secrets.NewIn(settings.From(ctx), d, d.Read, nil).Value(ctx, secrets.GoogleOAuthClientSecret)
	return &oauth2.Config{
		ClientID: clientID, ClientSecret: secret, RedirectURL: redirect, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: config.GoogleAuthURL(), TokenURL: config.GoogleTokenURL(), AuthStyle: oauth2.AuthStyleInParams},
	}
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
	cap, err := completeGoogle(h.ctx(r), h.d.DB, h.d.Env, page.ClientID, state, code, authErr)
	if err != nil {
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not connected", Description: page.Error}, page)
		return
	}
	w.Header().Set("Location", googlePath+"?connected="+cap.Name)
	w.WriteHeader(http.StatusSeeOther)
}

func completeGoogle(ctx context.Context, d *db.DB, env config.Env, clientID, state, code, authErr string) (googleCapability, error) {
	var verifier, capabilityName, redirect string
	var created float64
	err := d.Read.QueryRowContext(ctx, "SELECT verifier, capability, redirect_uri, created_ts FROM oauth_states WHERE state = ?", state).
		Scan(&verifier, &capabilityName, &redirect, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return googleCapability{}, errors.New("this consent link is unknown or already used; start again")
	}
	if err != nil {
		return googleCapability{}, err
	}
	_, _ = d.Write.ExecContext(ctx, "DELETE FROM oauth_states WHERE state = ?", state)
	if authErr != "" {
		return googleCapability{}, errors.New("Google returned an error: " + authErr)
	}
	if time.Since(time.Unix(int64(created), 0)) > googleStateTTL {
		return googleCapability{}, errors.New("the consent link expired; start again")
	}
	cap, ok := capability(capabilityName)
	if !ok {
		return googleCapability{}, errors.New("the consent link names an unknown capability")
	}
	conf := oauthConfig(ctx, d, clientID, redirect)
	exchange := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	token, err := conf.Exchange(exchange, code, oauth2.VerifierOption(verifier))
	if err != nil || token.RefreshToken == "" {
		if err == nil {
			err = errors.New("Google returned no refresh token; revoke the app's access and retry") //nolint:staticcheck // the CLI contract keeps this wording
		}
		_ = RecordResult(ctx, d, env, "google", "Google", NeedsReauth, err.Error(), stamp())
		return googleCapability{}, err
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		return secrets.NewIn(settings.From(ctx), d, tx, d).Set(ctx, cap.Secret, token.RefreshToken, "ui")
	}); err != nil {
		return googleCapability{}, err
	}
	if err := RecordResult(ctx, d, env, "google", "Google", OK, cap.Label+" connected", stamp()); err != nil {
		return googleCapability{}, err
	}
	return cap, nil
}

func (h handlers) googleTest(w http.ResponseWriter, r *http.Request) {
	ctx := h.ctx(r)
	page := h.googlePage(r)
	secret, _ := secrets.NewIn(settings.From(ctx), h.d.DB, h.d.DB.Read, nil).Value(ctx, secrets.GoogleCalendarRefreshToken)
	if secret == "" {
		page.Error = "Connect Google first."
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not tested", Description: page.Error}, page)
		return
	}
	state, reason, err := testGoogleRefresh(ctx, h.d.DB, h.d.Env, page.ClientID, secret)
	if err != nil {
		page.Error = err.Error()
		h.googleRender(w, r, http.StatusBadGateway, &tpl.Toast{Category: "error", Title: "Not verified", Description: page.Error}, page)
		return
	}
	if state != OK {
		page.Error = reason
		h.googleRender(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not verified", Description: reason}, page)
		return
	}
	h.googleRender(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connected", Description: "The refresh token works."}, h.googlePage(r))
}

func testGoogleRefresh(ctx context.Context, d *db.DB, env config.Env, clientID, refresh string) (string, string, error) {
	secret, _ := secrets.NewIn(settings.From(ctx), d, d.Read, nil).Value(ctx, secrets.GoogleOAuthClientSecret)
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID},
		"client_secret": {secret}, "refresh_token": {refresh}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.GoogleTokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		_ = RecordResult(ctx, d, env, "google", "Google", Failing, err.Error(), stamp())
		return Failing, err.Error(), nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var reply struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(body, &reply)
	if resp.StatusCode != http.StatusOK || reply.AccessToken == "" {
		state := Failing
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			state = NeedsReauth
		}
		reason := fmt.Sprintf("token endpoint answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		_ = RecordResult(ctx, d, env, "google", "Google", state, reason, stamp())
		return state, reason, nil
	}
	if err := RecordResult(ctx, d, env, "google", "Google", OK, "token refresh works", stamp()); err != nil {
		return "", "", err
	}
	return OK, "", nil
}

func token(n int) string {
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
