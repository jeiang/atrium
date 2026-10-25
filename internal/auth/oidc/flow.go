package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	loginTTL      = 10 * time.Minute
	loginCap      = 500
	slideInterval = time.Minute
	recheckGrace  = time.Hour
	retryGap      = time.Minute
	maxReturnLen  = 2048
)

var (
	log = logx.Logger("atrium.auth")

	ErrNoSession    = errors.New("oidc: no session")
	ErrLogin        = errors.New("oidc: login state is invalid")
	ErrLoginTimeout = errors.New("oidc: login state expired")
	ErrRevoked      = errors.New("oidc: access was removed")
	ErrDenied       = errors.New("oidc: account has no access")
)

type Service struct {
	d   *db.DB
	env config.Env
	hc  *http.Client
	now func() time.Time

	KeyRefetchGap time.Duration

	mu        sync.Mutex
	providers map[string]*provider
	locks     map[int64]*sync.Mutex
	retry     map[int64]time.Time
}

func New(d *db.DB, env config.Env) *Service {
	s := &Service{d: d, env: env, now: time.Now, KeyRefetchGap: jwksMinGap, providers: map[string]*provider{}, locks: map[int64]*sync.Mutex{}, retry: map[int64]time.Time{}}
	s.hc = s.client()
	return s
}

func (s *Service) Check(ctx context.Context) error {
	_, err := load(ctx, s.d.Read, s.env)
	return err
}

func (s *Service) Config(ctx context.Context) (Config, error) { return load(ctx, s.d.Read, s.env) }

func (s *Service) unix() float64 { return float64(s.now().UnixNano()) / 1e9 }

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func SafeReturn(target string) string {
	if target == "" || len(target) > maxReturnLen || target[0] != '/' || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/auth/") {
		return "/"
	}
	return target
}

type Login struct {
	URL     string
	Binding string
}

func (s *Service) Begin(ctx context.Context, returnTo string) (Login, error) {
	cfg, err := load(ctx, s.d.Read, s.env)
	if err != nil {
		return Login{}, err
	}
	p, err := s.provider(ctx, cfg.Issuer)
	if err != nil {
		return Login{}, err
	}
	state, nonce, verifier, binding := randomToken(), randomToken(), randomToken(), randomToken()
	now := s.unix()
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_logins WHERE created_at < ?", now-loginTTL.Seconds()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO auth_logins (state_hash, binding_hash, nonce, verifier, return_to, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			digest(state), digest(binding), nonce, verifier, SafeReturn(returnTo), now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM auth_logins WHERE state_hash NOT IN (SELECT state_hash FROM auth_logins ORDER BY created_at DESC LIMIT ?)", loginCap)
		return err
	})
	if err != nil {
		return Login{}, err
	}
	scopes := slices.Clone(cfg.Scopes)
	if p.offline && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {cfg.RedirectURI},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	for k, v := range cfg.Preset.AuthParams {
		q.Set(k, v)
	}
	target, err := url.Parse(p.authURL)
	if err != nil {
		return Login{}, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	existing := target.Query()
	for k, v := range q {
		existing[k] = v
	}
	target.RawQuery = existing.Encode()
	return Login{URL: target.String(), Binding: binding}, nil
}

type Session struct {
	ID      int64
	Subject string
	Email   string
	Name    string
	Admin   bool
}

type Grant struct {
	Token    string
	ReturnTo string
	MaxAge   time.Duration
	Session  Session
}

type pendingLogin struct {
	binding, nonce, verifier, returnTo string
	created                            float64
}

func (s *Service) consumeLogin(ctx context.Context, state string) (pendingLogin, error) {
	var p pendingLogin
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT binding_hash, nonce, verifier, return_to, created_at FROM auth_logins WHERE state_hash = ?", digest(state))
		if err := row.Scan(&p.binding, &p.nonce, &p.verifier, &p.returnTo, &p.created); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM auth_logins WHERE state_hash = ?", digest(state))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrLogin
	}
	return p, err
}

func (s *Service) Complete(ctx context.Context, state, code, binding string) (Grant, error) {
	if state == "" || code == "" || binding == "" {
		return Grant{}, ErrLogin
	}
	pending, err := s.consumeLogin(ctx, state)
	if err != nil {
		return Grant{}, err
	}
	if subtle.ConstantTimeCompare([]byte(pending.binding), []byte(digest(binding))) != 1 {
		return Grant{}, ErrLogin
	}
	if s.unix()-pending.created > loginTTL.Seconds() {
		return Grant{}, ErrLoginTimeout
	}
	cfg, err := load(ctx, s.d.Read, s.env)
	if err != nil {
		return Grant{}, err
	}
	p, err := s.provider(ctx, cfg.Issuer)
	if err != nil {
		return Grant{}, err
	}
	reply, err := s.tokens(ctx, cfg, p, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
		"code_verifier": {pending.verifier},
	})
	if err != nil {
		return Grant{}, err
	}
	if reply.IDToken == "" {
		return Grant{}, fmt.Errorf("%w: the token reply carries no id_token", ErrToken)
	}
	claims, err := s.verifyIDToken(ctx, p, cfg, reply.IDToken, claimRules{nonce: pending.nonce})
	if err != nil {
		return Grant{}, err
	}
	id, err := s.resolveIdentity(ctx, p, cfg, claims, reply.AccessToken)
	if err != nil {
		return Grant{}, err
	}
	access, admin := cfg.decide(id)
	if !access {
		log.Warn("sign-in refused: no access", "sub", id.Subject, "groups_present", id.HasGroups)
		return Grant{}, ErrDenied
	}
	token := randomToken()
	now := s.unix()
	var accessExpires any
	if reply.ExpiresIn > 0 {
		accessExpires = now + float64(reply.ExpiresIn)
	}
	var sess Session
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE absolute_expires_at < ? OR idle_expires_at < ?", now, now); err != nil {
			return err
		}
		absolute := now + cfg.Max.Seconds()
		idle := min(now+cfg.Idle.Seconds(), absolute)
		res, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions (token_hash, subject, email, name, admin, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rechecked_at, access_token, access_expires_at, refresh_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			digest(token), id.Subject, id.Email, id.Name, boolInt(admin), now, now, idle, absolute, now, reply.AccessToken, accessExpires, reply.RefreshToken)
		if err != nil {
			return err
		}
		sess.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	sess.Subject, sess.Email, sess.Name, sess.Admin = id.Subject, id.Email, id.Name, admin
	return Grant{Token: token, ReturnTo: pending.returnTo, MaxAge: cfg.Max, Session: sess}, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) resolveIdentity(ctx context.Context, p *provider, cfg Config, claims map[string]any, accessToken string) (identity, error) {
	id := identityOf(claims, cfg)
	if cfg.GroupsClaim == "" || id.HasGroups || p.userinfoURL == "" || accessToken == "" {
		return id, nil
	}
	info, err := s.userinfo(ctx, p, accessToken)
	if err != nil {
		return id, err
	}
	if claimString(info, "sub") != id.Subject {
		return id, fmt.Errorf("%w: userinfo subject differs from the id token", ErrToken)
	}
	extra := identityOf(info, cfg)
	id.Groups, id.HasGroups = extra.Groups, extra.HasGroups
	if id.Email == "" {
		id.Email, id.EmailVerified = extra.Email, extra.EmailVerified
	}
	if id.Name == "" {
		id.Name = extra.Name
	}
	return id, nil
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (s *Service) tokens(ctx context.Context, cfg Config, p *provider, form url.Values) (tokenReply, error) {
	basic := p.basicAuth && cfg.ClientSecret != ""
	if !basic {
		form.Set("client_id", cfg.ClientID)
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
	}
	req, err := http.NewRequest(http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	}
	status, body, err := s.fetch(ctx, req)
	if err != nil {
		return tokenReply{}, err
	}
	switch {
	case status >= 500 || status == http.StatusTooManyRequests:
		return tokenReply{}, fmt.Errorf("%w: token endpoint answered %d", ErrProvider, status)
	case status != http.StatusOK:
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &problem)
		return tokenReply{}, fmt.Errorf("%w: token endpoint answered %d %s", ErrRejected, status, sanitize(problem.Error))
	}
	var reply tokenReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return tokenReply{}, fmt.Errorf("%w: token reply is not JSON", ErrProvider)
	}
	if reply.AccessToken == "" {
		return tokenReply{}, fmt.Errorf("%w: token reply carries no access_token", ErrProvider)
	}
	if reply.TokenType != "" && !strings.EqualFold(reply.TokenType, "bearer") {
		return tokenReply{}, fmt.Errorf("%w: unsupported token type", ErrProvider)
	}
	return reply, nil
}

func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return -1
		}
		return r
	}, s)
}

func (s *Service) userinfo(ctx context.Context, p *provider, accessToken string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, p.userinfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	status, body, err := s.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	switch {
	case status >= 500 || status == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: userinfo answered %d", ErrProvider, status)
	case status != http.StatusOK:
		return nil, fmt.Errorf("%w: userinfo answered %d", ErrRejected, status)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil || claims == nil {
		return nil, fmt.Errorf("%w: userinfo is not JSON", ErrProvider)
	}
	return claims, nil
}
