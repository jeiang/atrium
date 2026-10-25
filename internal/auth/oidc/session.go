package oidc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/settings"
)

type row struct {
	Session
	idleExpires     float64
	absoluteExpires float64
	lastSeen        float64
	rechecked       float64
	accessToken     string
	accessExpires   sql.NullFloat64
	refreshToken    string
}

const sessionColumns = "id, subject, email, name, admin, idle_expires_at, absolute_expires_at, last_seen_at, rechecked_at, access_token, access_expires_at, refresh_token"

func (s *Service) loadRow(ctx context.Context, hash string) (row, error) {
	var r row
	var admin int
	err := s.d.Read.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM auth_sessions WHERE token_hash = ?", hash).
		Scan(&r.ID, &r.Subject, &r.Email, &r.Name, &admin, &r.idleExpires, &r.absoluteExpires, &r.lastSeen, &r.rechecked, &r.accessToken, &r.accessExpires, &r.refreshToken)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNoSession
	}
	r.Admin = admin == 1
	return r, err
}

type timing struct{ idle, recheck time.Duration }

func (s *Service) timing(ctx context.Context) (timing, error) {
	var t timing
	for _, field := range []struct {
		key  string
		unit time.Duration
		into *time.Duration
	}{{KeyIdleDays, 24 * time.Hour, &t.idle}, {KeyRecheckMinutes, time.Minute, &t.recheck}} {
		found, err := settings.Default.Resolve(ctx, s.d.Read, field.key, 0)
		if err != nil {
			return t, err
		}
		n, err := found.Int()
		if err != nil {
			return t, err
		}
		*field.into = time.Duration(n) * field.unit
	}
	return t, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" || len(token) > 128 {
		return Session{}, ErrNoSession
	}
	hash := digest(token)
	r, err := s.loadRow(ctx, hash)
	if err != nil {
		return Session{}, err
	}
	now := s.unix()
	if now >= r.absoluteExpires || now >= r.idleExpires {
		return Session{}, s.drop(ctx, r.ID)
	}
	t, err := s.timing(ctx)
	if err != nil {
		return Session{}, err
	}
	if now >= r.rechecked+t.recheck.Seconds() {
		if r, err = s.recheck(ctx, hash, r, t); err != nil {
			return Session{}, err
		}
	}
	if now-r.lastSeen >= slideInterval.Seconds() {
		idle := min(now+t.idle.Seconds(), r.absoluteExpires)
		if _, err := s.d.Write.ExecContext(ctx, "UPDATE auth_sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id = ?", now, idle, r.ID); err != nil {
			return Session{}, err
		}
	}
	return r.Session, nil
}

func (s *Service) drop(ctx context.Context, id int64) error {
	if _, err := s.d.Write.ExecContext(ctx, "DELETE FROM auth_sessions WHERE id = ?", id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.locks, id)
	delete(s.retry, id)
	s.mu.Unlock()
	return ErrNoSession
}

func (s *Service) End(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.d.Write.ExecContext(ctx, "DELETE FROM auth_sessions WHERE token_hash = ?", digest(token))
	return err
}

func (s *Service) lockFor(id int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.locks[id]
	if m == nil {
		m = new(sync.Mutex)
		s.locks[id] = m
	}
	return m
}

func (s *Service) recheck(ctx context.Context, hash string, seen row, t timing) (row, error) {
	lock := s.lockFor(seen.ID)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.loadRow(ctx, hash)
	if err != nil {
		return r, err
	}
	now := s.unix()
	if now < r.rechecked+t.recheck.Seconds() {
		return r, nil
	}
	cfg, err := load(ctx, s.d.Read, s.env)
	if err != nil {
		log.Warn("session ended: oidc configuration is unusable", "sub", r.Subject)
		return r, s.drop(ctx, r.ID)
	}
	if !cfg.rechecks() {
		return r, s.touchRecheck(ctx, r, now)
	}
	s.mu.Lock()
	next := s.retry[r.ID]
	s.mu.Unlock()
	if s.now().Before(next) {
		return r, nil
	}
	outcome, err := s.refresh(ctx, cfg, r)
	switch {
	case err == nil:
		r.Email, r.Name, r.Admin = outcome.Email, outcome.Name, outcome.Admin
		r.accessToken, r.refreshToken, r.accessExpires = outcome.accessToken, outcome.refreshToken, outcome.accessExpires
		if err := s.saveRecheck(ctx, r, now); err != nil {
			return r, err
		}
		r.rechecked = now
		return r, nil
	case errors.Is(err, ErrProvider) && now < r.rechecked+t.recheck.Seconds()+recheckGrace.Seconds():
		log.Warn("group re-check could not reach the provider; keeping the session for now", "sub", r.Subject)
		s.mu.Lock()
		s.retry[r.ID] = s.now().Add(retryGap)
		s.mu.Unlock()
		return r, nil
	default:
		log.Warn("session ended by group re-check", "sub", r.Subject, "reason", describe(err))
		return r, s.drop(ctx, r.ID)
	}
}

func describe(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return "no longer has access"
	case errors.Is(err, ErrRejected):
		return "provider rejected the refresh"
	case errors.Is(err, ErrToken):
		return "id token refused"
	case errors.Is(err, ErrProvider):
		return "provider unavailable past the grace period"
	}
	return "re-check failed"
}

type recheckOutcome struct {
	Session
	accessToken   string
	refreshToken  string
	accessExpires sql.NullFloat64
}

func (s *Service) refresh(ctx context.Context, cfg Config, r row) (recheckOutcome, error) {
	out := recheckOutcome{Session: r.Session, accessToken: r.accessToken, refreshToken: r.refreshToken, accessExpires: r.accessExpires}
	p, err := s.provider(ctx, cfg.Issuer)
	if err != nil {
		return out, err
	}
	var claims map[string]any
	access := r.accessToken
	if r.refreshToken != "" {
		reply, err := s.tokens(ctx, cfg, p, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.refreshToken}})
		if err != nil {
			return out, err
		}
		access = reply.AccessToken
		out.accessToken = reply.AccessToken
		if reply.RefreshToken != "" {
			out.refreshToken = reply.RefreshToken
		}
		out.accessExpires = sql.NullFloat64{}
		if reply.ExpiresIn > 0 {
			out.accessExpires = sql.NullFloat64{Float64: s.unix() + float64(reply.ExpiresIn), Valid: true}
		}
		if reply.IDToken != "" {
			if claims, err = s.verifyIDToken(ctx, p, cfg, reply.IDToken, claimRules{subject: r.Subject}); err != nil {
				return out, err
			}
		}
	} else if r.accessExpires.Valid && s.unix() >= r.accessExpires.Float64 {
		return out, fmt.Errorf("%w: the access token expired and no refresh token was issued", ErrRejected)
	}
	var id identity
	if claims != nil {
		id = identityOf(claims, cfg)
	} else {
		id = identity{Subject: r.Subject}
	}
	if !id.HasGroups {
		if p.userinfoURL == "" {
			return out, fmt.Errorf("%w: no group information available", ErrDenied)
		}
		info, err := s.userinfo(ctx, p, access)
		if err != nil {
			return out, err
		}
		if claimString(info, "sub") != r.Subject {
			return out, fmt.Errorf("%w: userinfo subject differs from the session", ErrToken)
		}
		extra := identityOf(info, cfg)
		id.Groups, id.HasGroups = extra.Groups, extra.HasGroups
		if id.Email == "" {
			id.Email, id.EmailVerified = extra.Email, extra.EmailVerified
		}
		if id.Name == "" {
			id.Name = extra.Name
		}
	}
	if id.Name == "" {
		id.Name = r.Name
	}
	granted, admin := cfg.decide(id)
	if !granted {
		return out, ErrDenied
	}
	if id.Email != "" {
		out.Email = id.Email
	}
	out.Name, out.Admin = id.Name, admin
	return out, nil
}

func (s *Service) touchRecheck(ctx context.Context, r row, now float64) error {
	_, err := s.d.Write.ExecContext(ctx, "UPDATE auth_sessions SET rechecked_at = ? WHERE id = ?", now, r.ID)
	return err
}

func (s *Service) saveRecheck(ctx context.Context, r row, now float64) error {
	_, err := s.d.Write.ExecContext(ctx, "UPDATE auth_sessions SET rechecked_at = ?, admin = ?, email = ?, name = ?, access_token = ?, access_expires_at = ?, refresh_token = ? WHERE id = ?",
		now, boolInt(r.Admin), r.Email, r.Name, r.accessToken, r.accessExpires, r.refreshToken, r.ID)
	return err
}
