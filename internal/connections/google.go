package connections

import (
	"context"
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

	"github.com/jeiang/atrium/internal/config"
)

const (
	GoogleID       ID   = "google/default"
	KindGoogle     Kind = "google"
	GoogleCalendar      = "calendar"
	GoogleGmail         = "gmail"
)

const (
	googleCalendarScope = "https://www.googleapis.com/auth/calendar.events"
	googleGmailScope    = "https://mail.google.com/"
	GoogleStateTTL      = 10 * time.Minute
)

type GoogleGrant struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Scope  string `json:"scope"`
	Slot   string `json:"slot"`
	Shared string `json:"shared"`
}

var googleGrantList = []GoogleGrant{
	{Name: GoogleCalendar, Label: "Calendar", Scope: googleCalendarScope, Slot: "oauth/calendar/refresh"},
	{Name: GoogleGmail, Label: "Gmail", Scope: googleGmailScope, Slot: "oauth/gmail/refresh"},
}

func GoogleGrantFor(name string) (GoogleGrant, bool) {
	for _, g := range googleGrantList {
		if g.Name == name {
			return g, true
		}
	}
	return GoogleGrant{}, false
}

func GoogleGrants() []GoogleGrant {
	out := make([]GoogleGrant, len(googleGrantList))
	copy(out, googleGrantList)
	return out
}

type OAuthState struct {
	Verifier   string
	Capability string
	Redirect   string
	Created    time.Time
}

var ErrUnknownConsent = errors.New("this consent link is unknown or already used; start again")

func (s *Store) SaveOAuthState(ctx context.Context, state, verifier, capability, redirect string, commits ...Commit) error {
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, "DELETE FROM oauth_states WHERE created_ts < ?", float64(now.Add(-GoogleStateTTL).UnixNano())/1e9); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO oauth_states (state, verifier, capability, redirect_uri, created_ts) VALUES (?, ?, ?, ?, ?)",
			state, verifier, capability, redirect, float64(now.UnixNano())/1e9); err != nil {
			return err
		}
		return committed(ctx, tx, true, commits)
	})
}

func (s *Store) ConsumeOAuthState(ctx context.Context, state string) (OAuthState, error) {
	var out OAuthState
	var created float64
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "DELETE FROM oauth_states WHERE state = ? RETURNING verifier, capability, redirect_uri, created_ts", state).
			Scan(&out.Verifier, &out.Capability, &out.Redirect, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownConsent
		}
		return err
	})
	if err != nil {
		return OAuthState{}, err
	}
	out.Created = time.Unix(int64(created), 0)
	return out, nil
}

func Connections() []Definition {
	return []Definition{{Kind: KindGoogle, Validate: validateGoogle, Test: testGoogle}}
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
		if _, ok := GoogleGrantFor(cap.Name); !ok {
			return fmt.Errorf("google has no %s capability", cap.Name)
		}
	}
	return nil
}

func GoogleConfig(lease *Lease) (clientID, authURL, tokenURL, apiURL string, err error) {
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
	grant, ok := GoogleGrantFor(capability)
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

func (l *Lease) refreshGoogle(ctx context.Context, grant GoogleGrant) (*googleAccess, error) {
	clientID, _, tokenURL, _, err := GoogleConfig(l)
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
		grant, ok := GoogleGrantFor(cap.Name)
		if !ok {
			results = append(results, Result{Capability: cap.Name, State: Failing, Reason: "unknown Google capability"})
			continue
		}
		lease.InvalidateToken(grant.Name)
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
	_, _, _, apiURL, err := GoogleConfig(lease)
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
	switch resp.StatusCode {
	case http.StatusOK:
		data, _ := json.Marshal(map[string]any{"max_results": 1})
		return Result{Capability: GoogleCalendar, State: OK, Reason: "calendar read works", Data: data, ExpiresAt: lease.googleExpiry(GoogleCalendar)}
	case http.StatusUnauthorized:
		return Result{Capability: GoogleCalendar, State: NeedsReauth, Reason: "Google refused the access token"}
	case http.StatusForbidden:
		return Result{Capability: GoogleCalendar, State: Failing, Reason: "Google refused the calendar scope"}
	default:
		return Result{Capability: GoogleCalendar, State: Failing, Reason: fmt.Sprintf("calendar endpoint answered %d", resp.StatusCode)}
	}
}

// EnsureGoogle materializes the single Google connection and its two grants.
func (s *Store) EnsureGoogle(ctx context.Context) error {
	caps := make([]Capability, 0, len(googleGrantList))
	for _, g := range googleGrantList {
		caps = append(caps, Capability{Name: g.Name, Enabled: true, Config: json.RawMessage("{}")})
	}
	return s.Ensure(ctx, GoogleID, KindGoogle, "Google", nil, caps)
}
