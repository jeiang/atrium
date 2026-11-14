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
	id int64
	Session
	idleExpires     float64
	absoluteExpires float64
	lastSeen        float64
	rechecked       float64
	accessToken     string
	emailVerified   bool
	hosted          string
	accessExpires   sql.NullFloat64
	refreshToken    string
}

type sessionInsert struct {
	tokenHash     string
	subject       string
	email         string
	name          string
	now           float64
	idle          float64
	absolute      float64
	rechecked     float64
	accessToken   string
	accessExpires any
	refreshToken  string
	verified      bool
	hosted        string
}

func insertSession(ctx context.Context, tx *sql.Tx, s sessionInsert) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions (token_hash, subject, email, name, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rechecked_at, access_token, access_expires_at, refresh_token, email_verified, hosted_domain) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.tokenHash, s.subject, s.email, s.name, s.now, s.now, s.idle, s.absolute, s.rechecked, s.accessToken, s.accessExpires, s.refreshToken, boolInt(s.verified), s.hosted)
	return err
}

const sessionColumns = "id, subject, email, name, idle_expires_at, absolute_expires_at, last_seen_at, rechecked_at, access_token, access_expires_at, refresh_token, email_verified, hosted_domain"

func (s *Service) loadRow(ctx context.Context, hash string) (row, error) {
	var r row
	var verified int
	err := s.d.Read.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM auth_sessions WHERE token_hash = ?", hash).
		Scan(&r.id, &r.Subject, &r.Email, &r.Name, &r.idleExpires, &r.absoluteExpires, &r.lastSeen, &r.rechecked, &r.accessToken, &r.accessExpires, &r.refreshToken, &verified, &r.hosted)
	r.emailVerified = verified == 1
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNoSession
	}
	return r, err
}

type timing struct{ idle, recheck time.Duration }

func (s *Service) timing(ctx context.Context) (timing, error) {
	var t timing
	found, err := settings.From(ctx).Resolve(ctx, s.d.Read, KeyIdleDays)
	if err != nil {
		return t, err
	}
	n, err := found.Int()
	if err != nil {
		return t, err
	}
	t.idle = time.Duration(n) * 24 * time.Hour
	t.recheck = TuningOidcRecheckMinutes * time.Minute
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
		return Session{}, s.drop(ctx, r.id)
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
		if _, err := s.d.Write.ExecContext(ctx, "UPDATE auth_sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id = ?", now, idle, r.id); err != nil {
			return Session{}, err
		}
	}
	return r.Session, nil
}

func (s *Service) forget(ids ...int64) {
	s.mu.Lock()
	for _, id := range ids {
		delete(s.locks, id)
		delete(s.retry, id)
	}
	s.mu.Unlock()
}

func expiredIDs(ctx context.Context, tx *sql.Tx, now float64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM auth_sessions WHERE absolute_expires_at < ? OR idle_expires_at < ?", now, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) drop(ctx context.Context, id int64) error {
	if _, err := s.d.Write.ExecContext(ctx, "DELETE FROM auth_sessions WHERE id = ?", id); err != nil {
		return err
	}
	s.forget(id)
	return ErrNoSession
}

func (s *Service) End(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	var id int64
	err := s.d.Read.QueryRowContext(ctx, "SELECT id FROM auth_sessions WHERE token_hash = ?", digest(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.d.Write.ExecContext(ctx, "DELETE FROM auth_sessions WHERE id = ?", id); err != nil {
		return err
	}
	s.forget(id)
	return nil
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
	lock := s.lockFor(seen.id)
	lock.Lock()
	defer lock.Unlock()
	r, err := s.loadRow(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			s.forget(seen.id)
		}
		return r, err
	}
	now := s.unix()
	if now < r.rechecked+t.recheck.Seconds() {
		return r, nil
	}
	cfg, err := s.load(ctx)
	if err != nil {
		var config *ConfigError
		if errors.As(err, &config) {
			log.Warn("session ended: oidc configuration is unusable", "sub", r.Subject)
			return r, s.drop(ctx, r.id)
		}
		// A transient read failure leaves the last authorized identity in
		// place only for the same bounded grace a provider outage gets; after
		// that the session is dropped without a fresh authorization decision.
		log.Warn("re-check could not read the oidc configuration; keeping the session for now", "sub", r.Subject, "err", err.Error())
		s.mu.Lock()
		s.retry[r.id] = s.now().Add(retryGap)
		s.mu.Unlock()
		if now < r.rechecked+t.recheck.Seconds()+recheckGrace.Seconds() {
			return r, nil
		}
		return r, s.drop(ctx, r.id)
	}
	s.mu.Lock()
	next := s.retry[r.id]
	s.mu.Unlock()
	if s.now().Before(next) && now < r.rechecked+t.recheck.Seconds()+recheckGrace.Seconds() {
		return r, nil
	}
	sl := s.sealerFor(cfg)
	r.accessToken, r.refreshToken = sl.open(r.accessToken), sl.open(r.refreshToken)
	outcome, err := s.refresh(ctx, cfg, r)
	switch {
	case err == nil:
		r.Email, r.Name = outcome.Email, outcome.Name
		r.accessToken, r.refreshToken, r.accessExpires = outcome.accessToken, outcome.refreshToken, outcome.accessExpires
		r.emailVerified, r.hosted = outcome.verified, outcome.hosted
		if err := s.saveRecheck(ctx, sl, r, now); err != nil {
			return r, err
		}
		r.rechecked = now
		return r, nil
	case errors.Is(err, ErrProvider) && now < r.rechecked+t.recheck.Seconds()+recheckGrace.Seconds():
		log.Warn("group re-check could not reach the provider; keeping the session for now", "sub", r.Subject)
		s.mu.Lock()
		s.retry[r.id] = s.now().Add(retryGap)
		s.mu.Unlock()
		return r, nil
	default:
		log.Warn("session ended by group re-check", "sub", r.Subject, "reason", describe(err))
		dropped := s.drop(ctx, r.id)
		if errors.Is(err, ErrDenied) && errors.Is(dropped, ErrNoSession) {
			return r, ErrRevoked
		}
		return r, dropped
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
	verified      bool
	hosted        string
}

func (s *Service) refresh(ctx context.Context, cfg Config, r row) (recheckOutcome, error) {
	out := recheckOutcome{Session: r.Session, accessToken: r.accessToken, refreshToken: r.refreshToken, accessExpires: r.accessExpires, verified: r.emailVerified, hosted: r.hosted}
	needGroups := cfg.GroupsClaim != ""
	stored := identity{Subject: r.Subject, Email: r.Email, EmailVerified: r.emailVerified, Hosted: r.hosted, Name: r.Name}
	accessExpired := r.accessExpires.Valid && s.unix() >= r.accessExpires.Float64
	if r.refreshToken == "" && (accessExpired || r.accessToken == "") {
		if needGroups {
			return out, fmt.Errorf("%w: no way to ask the provider for groups", ErrRejected)
		}
		return s.decideRecheck(cfg, stored, out)
	}
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
	}
	id := stored
	if claims != nil {
		id = id.signed(claims, cfg)
	}
	if needsUserinfo(id, cfg) && p.userinfoURL != "" {
		info, err := s.userinfo(ctx, p, access)
		if err != nil {
			return out, err
		}
		if claimString(info, "sub") != r.Subject {
			return out, fmt.Errorf("%w: userinfo subject differs from the session", ErrToken)
		}
		id = id.absorb(info, cfg)
	}
	if needGroups && !id.HasGroups {
		return out, fmt.Errorf("%w: no group information available", ErrDenied)
	}
	return s.decideRecheck(cfg, id, out)
}

func (s *Service) decideRecheck(cfg Config, id identity, out recheckOutcome) (recheckOutcome, error) {
	if !cfg.decide(id) {
		return out, ErrDenied
	}
	out.Email, out.Name = id.Email, id.Name
	out.verified, out.hosted = id.EmailVerified, id.Hosted
	return out, nil
}

func (s *Service) saveRecheck(ctx context.Context, sl *sealer, r row, now float64) error {
	_, err := s.d.Write.ExecContext(ctx, "UPDATE auth_sessions SET rechecked_at = ?, email = ?, name = ?, access_token = ?, access_expires_at = ?, refresh_token = ?, email_verified = ?, hosted_domain = ? WHERE id = ?",
		now, r.Email, r.Name, sl.seal(r.accessToken), r.accessExpires, sl.seal(r.refreshToken), boolInt(r.emailVerified), r.hosted, r.id)
	return err
}
