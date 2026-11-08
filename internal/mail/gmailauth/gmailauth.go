package gmailauth

import (
	"context"
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
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/accounts"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	DefaultTokenURL = "https://oauth2.googleapis.com/token"
	ClientIDKey     = "google_client_id"
	ExpiryWindow    = 14 * 24 * time.Hour
)

type HealthFunc func(ctx context.Context, state, reason string)

// Source refreshes a Gmail access token from the stored refresh token. One
// refresh runs at a time; concurrent callers wait for its result.
type Source struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	TokenURL     string
	HTTP         *http.Client
	Now          func() time.Time
	Health       HealthFunc

	mu      sync.Mutex
	token   string
	expiry  time.Time
	waiting chan struct{}
	lastErr error
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.token != "" && s.now().Before(s.expiry.Add(-time.Minute)) {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	if waiting := s.waiting; waiting != nil {
		s.mu.Unlock()
		select {
		case <-waiting:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		s.mu.Lock()
		token, err := s.token, s.lastErr
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}
	waiting := make(chan struct{})
	s.waiting = waiting
	s.mu.Unlock()

	token, expiry, err := s.refresh(ctx)

	s.mu.Lock()
	s.waiting = nil
	s.lastErr = err
	if err == nil {
		s.token, s.expiry = token, expiry
	}
	close(waiting)
	s.mu.Unlock()
	if err != nil {
		s.report(ctx, "needs_reauth", err.Error())
		return "", err
	}
	if expiry.Sub(s.now()) < ExpiryWindow {
		s.report(ctx, "expiring", "access token expires "+expiry.Format(time.DateOnly))
	}
	return token, nil
}

func (s *Source) report(ctx context.Context, state, reason string) {
	if s.Health != nil {
		s.Health(ctx, state, reason)
	}
}

func (s *Source) refresh(ctx context.Context) (string, time.Time, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {s.ClientID},
		"client_secret": {s.ClientSecret},
		"refresh_token": {s.RefreshToken},
	}
	endpoint := s.TokenURL
	if endpoint == "" {
		endpoint = DefaultTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, err
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
		return "", time.Time{}, fmt.Errorf("token endpoint answered %d: %s", resp.StatusCode, reason)
	}
	expiry := s.now().Add(time.Hour)
	if reply.ExpiresIn > 0 {
		expiry = s.now().Add(time.Duration(reply.ExpiresIn) * time.Second)
	}
	return reply.AccessToken, expiry, nil
}

// Resolve returns the account's auth method and, for a Gmail XOAUTH2 account,
// the token source built from the Google client and the wizard's refresh token.
func Resolve(ctx context.Context, q db.Querier, account string) (accounts.Method, *Source, error) {
	spec, ok := config.Account(account)
	if !ok {
		return "", nil, errors.New("unknown mail account " + account)
	}
	if spec.Preset != config.Gmail || spec.AuthMethod != accounts.XOAUTH2 {
		return accounts.Password, nil, nil
	}
	found, err := settings.Default.Resolve(ctx, q, ClientIDKey)
	if err != nil {
		return "", nil, err
	}
	store := secrets.NewIn(nil, q, nil)
	secret, err := store.Value(ctx, secrets.GoogleOAuthClientSecret)
	if err != nil {
		return "", nil, err
	}
	refresh, err := store.Value(ctx, secrets.GoogleGmailRefreshToken)
	if err != nil {
		return "", nil, err
	}
	var missing []string
	if found.Value == "" {
		missing = append(missing, ClientIDKey)
	}
	if secret == "" {
		missing = append(missing, secrets.GoogleOAuthClientSecret)
	}
	if refresh == "" {
		missing = append(missing, secrets.GoogleGmailRefreshToken)
	}
	if len(missing) > 0 {
		return "", nil, errors.New(strings.Join(missing, ", ") + " not set")
	}
	return accounts.XOAUTH2, &Source{ClientID: found.Value, ClientSecret: secret, RefreshToken: refresh, TokenURL: config.GoogleTokenURL()}, nil
}
