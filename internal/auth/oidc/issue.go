package oidc

import (
	"context"
	"database/sql"
	"time"
)

const (
	originOIDC = "oidc"
	originLink = "link"

	LinkSessionMax = 8 * time.Hour
	linkSubject    = "login-link"
)

func (s *Service) IssueLinkSession(ctx context.Context) (Grant, error) {
	t, err := s.timing(ctx)
	if err != nil {
		return Grant{}, err
	}
	token := randomToken()
	now := s.unix()
	absolute := now + LinkSessionMax.Seconds()
	idle := min(now+t.idle.Seconds(), absolute)
	var sess Session
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE absolute_expires_at < ? OR idle_expires_at < ?", now, now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions (token_hash, subject, admin, created_at, last_seen_at, idle_expires_at, absolute_expires_at, rechecked_at, origin) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?)",
			digest(token), linkSubject, now, now, idle, absolute, now, originLink)
		if err != nil {
			return err
		}
		sess.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Grant{}, err
	}
	sess.Subject, sess.Admin = linkSubject, true
	return Grant{Token: token, MaxAge: LinkSessionMax, Session: sess}, nil
}
