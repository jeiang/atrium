package oidc

import (
	"context"
	"database/sql"
	"time"
)

const (
	originLink = "link"

	LinkSessionMax = 8 * time.Hour
	linkSubject    = "login-link"
)

func (s *Service) IssueLinkSession(ctx context.Context) (Grant, error) {
	var grant Grant
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		grant, err = s.IssueLinkSessionTx(ctx, tx)
		return err
	})
	return grant, err
}

func (s *Service) IssueLinkSessionTx(ctx context.Context, tx *sql.Tx) (Grant, error) {
	t, err := s.timing(ctx)
	if err != nil {
		return Grant{}, err
	}
	token := randomToken()
	now := s.unix()
	absolute := now + LinkSessionMax.Seconds()
	idle := min(now+t.idle.Seconds(), absolute)
	expired, err := expiredIDs(ctx, tx, now)
	if err != nil {
		return Grant{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE absolute_expires_at < ? OR idle_expires_at < ?", now, now); err != nil {
		return Grant{}, err
	}
	s.forget(expired...)
	if _, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions (token_hash, subject, admin, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rechecked_at, origin) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?)",
		digest(token), linkSubject, now, now, idle, absolute, now, originLink); err != nil {
		return Grant{}, err
	}
	return Grant{Token: token, MaxAge: LinkSessionMax}, nil
}
