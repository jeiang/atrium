package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	discoveryTTL = time.Hour
	jwksTTL      = 6 * time.Hour
	jwksMinGap   = 30 * time.Second
	httpTimeout  = 10 * time.Second
	maxBody      = 1 << 20
)

var (
	ErrProvider = errors.New("oidc: provider unavailable")
	ErrNoPKCE   = errors.New("oidc: provider does not advertise S256 PKCE")
	ErrRejected = errors.New("oidc: provider rejected the request")
)

type provider struct {
	issuer      string
	authURL     string
	tokenURL    string
	jwksURI     string
	userinfoURL string
	basicAuth   bool
	offline     bool
	fetched     time.Time

	mu          sync.Mutex
	keys        []namedKey
	keysFetched time.Time
}

type discoveryDoc struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported       []string `json:"scopes_supported"`
}

func (s *Service) client() *http.Client {
	return &http.Client{
		Timeout:       httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s *Service) fetch(ctx context.Context, req *http.Request) (int, []byte, error) {
	resp, err := s.hc.Do(req.WithContext(ctx))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrProvider, unwrapURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return resp.StatusCode, nil, fmt.Errorf("%w: unreadable or oversized reply", ErrProvider)
	}
	return resp.StatusCode, body, nil
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (s *Service) provider(ctx context.Context, issuer string) (*provider, error) {
	s.mu.Lock()
	cached := s.providers[issuer]
	s.mu.Unlock()
	if cached != nil && s.now().Sub(cached.fetched) < discoveryTTL {
		return cached, nil
	}
	fresh, err := s.discover(ctx, issuer)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached != nil && cached.sameEndpoints(fresh) {
		cached.fetched = fresh.fetched
		return cached, nil
	}
	s.providers[issuer] = fresh
	return fresh, nil
}

func (p *provider) sameEndpoints(o *provider) bool {
	return p.authURL == o.authURL && p.tokenURL == o.tokenURL && p.jwksURI == o.jwksURI && p.userinfoURL == o.userinfoURL
}

func (s *Service) discover(ctx context.Context, issuer string) (*provider, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	req.Header.Set("Accept", "application/json")
	status, body, err := s.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: discovery answered %d", ErrProvider, status)
	}
	var doc discoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%w: discovery is not JSON", ErrProvider)
	}
	if doc.Issuer != issuer {
		return nil, fmt.Errorf("%w: discovery issuer %q does not match the configured issuer", ErrProvider, doc.Issuer)
	}
	for name, endpoint := range map[string]string{"authorization_endpoint": doc.AuthorizationEndpoint, "token_endpoint": doc.TokenEndpoint, "jwks_uri": doc.JWKSURI} {
		if checkEndpointURL(endpoint) != nil {
			return nil, fmt.Errorf("%w: discovery %s is missing or not https", ErrProvider, name)
		}
	}
	if doc.UserinfoEndpoint != "" && checkEndpointURL(doc.UserinfoEndpoint) != nil {
		return nil, fmt.Errorf("%w: discovery userinfo_endpoint is not https", ErrProvider)
	}
	if !slices.Contains(doc.CodeChallengeMethods, "S256") {
		return nil, ErrNoPKCE
	}
	basic := len(doc.TokenAuthMethods) == 0 || slices.Contains(doc.TokenAuthMethods, "client_secret_basic")
	if !basic && !slices.Contains(doc.TokenAuthMethods, "client_secret_post") && !slices.Contains(doc.TokenAuthMethods, "none") {
		return nil, fmt.Errorf("%w: no supported token endpoint authentication method", ErrProvider)
	}
	return &provider{
		issuer: issuer, authURL: doc.AuthorizationEndpoint, tokenURL: doc.TokenEndpoint, jwksURI: doc.JWKSURI,
		userinfoURL: doc.UserinfoEndpoint, basicAuth: basic, offline: slices.Contains(doc.ScopesSupported, "offline_access"),
		fetched: s.now(),
	}, nil
}

func checkEndpointURL(raw string) error {
	if raw == "" {
		return errors.New("empty")
	}
	return checkEndpoint(raw)
}

func (s *Service) keysFor(ctx context.Context, p *provider, t rawToken) ([]namedKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stale := p.keys == nil || s.now().Sub(p.keysFetched) > jwksTTL
	missing := !t.hasKey(p.keys) && s.now().Sub(p.keysFetched) > jwksMinGap
	if !stale && !missing {
		return p.keys, nil
	}
	req, err := http.NewRequest(http.MethodGet, p.jwksURI, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	req.Header.Set("Accept", "application/json")
	status, body, err := s.fetch(ctx, req)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%w: jwks answered %d", ErrProvider, status)
	}
	var keys []namedKey
	if err == nil {
		keys, err = parseJWKS(body)
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrProvider, err)
		}
	}
	if err != nil {
		if p.keys != nil && !stale {
			p.keysFetched = s.now()
			return p.keys, nil
		}
		return nil, err
	}
	p.keys, p.keysFetched = keys, s.now()
	return p.keys, nil
}

func (s *Service) verifyIDToken(ctx context.Context, p *provider, cfg Config, raw string, rules claimRules) (map[string]any, error) {
	t, err := splitToken(raw)
	if err != nil {
		return nil, err
	}
	keys, err := s.keysFor(ctx, p, t)
	if err != nil {
		return nil, err
	}
	if err := t.checkSignature(keys); err != nil {
		return nil, err
	}
	rules.issuer, rules.audience, rules.now = cfg.Issuer, cfg.ClientID, s.now()
	if err := t.checkClaims(rules); err != nil {
		return nil, err
	}
	return t.claims, nil
}
