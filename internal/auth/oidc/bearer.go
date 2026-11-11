package oidc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const bearerCacheMax = 1024

var ErrAPIOff = errors.New("oidc: api access is not configured")

type bearerEntry struct {
	session Session
	until   time.Time
}

func accessTokenType(typ string) bool {
	typ = strings.ToLower(strings.TrimSpace(typ))
	return typ == "at+jwt" || typ == "application/at+jwt"
}

func (s *Service) APIClientID(ctx context.Context) (string, error) {
	lease, err := s.lease(ctx)
	if err != nil {
		return "", nil
	}
	defer lease.Close()
	return strings.TrimSpace(configValues(lease)[cfgAPIClientID]), nil
}

func (s *Service) AuthenticateBearer(ctx context.Context, raw string) (Session, error) {
	audience, err := s.APIClientID(ctx)
	if err != nil {
		return Session{}, err
	}
	if audience == "" {
		return Session{}, ErrAPIOff
	}
	cfg, err := s.Config(ctx)
	var problems *ConfigError
	if errors.As(err, &problems) {
		return Session{}, fmt.Errorf("%w: %w", ErrAPIOff, err)
	}
	if err != nil {
		return Session{}, err
	}
	if raw == "" || len(raw) > maxTokenBytes {
		return Session{}, tokenError("malformed bearer token")
	}
	key := digest(audience + "\x00" + cfg.Issuer + "\x00" + raw)
	now := s.now()
	if hit, ok := s.cachedBearer(key, now); ok {
		return hit, nil
	}
	t, err := splitToken(raw)
	if err != nil {
		return Session{}, err
	}
	if !accessTokenType(t.header.Typ) {
		return Session{}, tokenError("not an access token")
	}
	p, err := s.provider(ctx, cfg.Issuer)
	if err != nil {
		return Session{}, err
	}
	keys, err := s.keysFor(ctx, p, t)
	if err != nil {
		return Session{}, err
	}
	if err := t.checkSignature(keys); err != nil {
		return Session{}, err
	}
	if err := t.checkClaims(claimRules{issuer: cfg.Issuer, audience: audience, now: now, access: true}); err != nil {
		return Session{}, err
	}
	id, err := s.resolveIdentity(ctx, p, cfg, t.claims, raw)
	if errors.Is(err, ErrRejected) {
		return Session{}, fmt.Errorf("%w: %w", ErrToken, err)
	}
	if err != nil {
		return Session{}, err
	}
	if !cfg.decide(id) {
		return Session{}, ErrDenied
	}
	timings, err := s.timing(ctx)
	if err != nil {
		return Session{}, err
	}
	exp, _ := numeric(t.claims["exp"])
	until := exp.Add(clockLeeway)
	if limit := now.Add(timings.recheck); limit.Before(until) {
		until = limit
	}
	session := Session{Subject: id.Subject, Email: id.Email, Name: id.Name}
	s.storeBearer(key, bearerEntry{session: session, until: until}, now)
	return session, nil
}

func EffectiveExpiry(raw string) (time.Time, bool) {
	t, err := splitToken(raw)
	if err != nil {
		return time.Time{}, false
	}
	exp, ok := numeric(t.claims["exp"])
	if !ok {
		return time.Time{}, false
	}
	return exp.Add(clockLeeway), true
}

func (s *Service) cachedBearer(key string, now time.Time) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bearer[key]
	if !ok {
		return Session{}, false
	}
	if !now.Before(e.until) {
		delete(s.bearer, key)
		return Session{}, false
	}
	return e.session, true
}

func (s *Service) storeBearer(key string, e bearerEntry, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bearer) >= bearerCacheMax {
		for k, old := range s.bearer {
			if !now.Before(old.until) {
				delete(s.bearer, k)
			}
		}
	}
	if len(s.bearer) >= bearerCacheMax {
		for k := range s.bearer {
			delete(s.bearer, k)
			break
		}
	}
	s.bearer[key] = e
}
