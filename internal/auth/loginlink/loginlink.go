package loginlink

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	DefaultTTL = 15 * time.Minute
	MaxTTL     = time.Hour
	Path       = "/login/link"

	auditKey   = "login_link"
	macContext = "atrium-login-link-v1"
	keepIssued = 24 * time.Hour
)

var (
	ErrInvalid = errors.New("loginlink: link is not valid")
	ErrUsed    = errors.New("loginlink: link was already used")
	ErrExpired = errors.New("loginlink: link expired")
)

type Service struct {
	d   *db.DB
	Now func() time.Time
}

func New(d *db.DB) *Service { return &Service{d: d, Now: time.Now} }

func (s *Service) unix() float64 { return float64(s.Now().UnixNano()) / 1e9 }

func (s *Service) mac(ctx context.Context, nonce string) ([]byte, error) {
	key, err := secrets.New(s.d, s.d).GeneratedKey(ctx, secrets.ActionLinkKey)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(macContext))
	m.Write([]byte(nonce))
	return m.Sum(nil), nil
}

func digest(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

func note(ctx context.Context, q db.Querier, format string, args ...any) error {
	text := fmt.Sprintf(format, args...)
	return audit.Record(ctx, q, audit.Entry{Key: auditKey, New: &text})
}

func (s *Service) Issue(ctx context.Context, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > MaxTTL {
		return "", fmt.Errorf("loginlink: the lifetime must be between a second and %s", MaxTTL)
	}
	ctx = audit.With(ctx, "operator", audit.CLI)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	sig, err := s.mac(ctx, nonce)
	if err != nil {
		return "", err
	}
	now := s.unix()
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_login_links WHERE expires_at < ?", now-keepIssued.Seconds()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO auth_login_links (nonce_hash, issued_at, expires_at) VALUES (?, ?, ?)", digest(nonce), now, now+ttl.Seconds())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return note(ctx, tx, "issued #%d for %s", id, ttl)
	})
	if err != nil {
		return "", err
	}
	token := nonce + "." + base64.RawURLEncoding.EncodeToString(sig)
	logx.AddSecret(token)
	logx.AddSecret(nonce)
	return token, nil
}

func (s *Service) Redeem(ctx context.Context, token string) error {
	ctx = audit.With(ctx, "login-link", audit.UI)
	nonce, given, ok := strings.Cut(token, ".")
	if !ok || len(nonce) > 128 || len(given) > 128 {
		return ErrInvalid
	}
	want, err := s.mac(ctx, nonce)
	if err != nil {
		return err
	}
	got, err := base64.RawURLEncoding.DecodeString(given)
	if err != nil || !hmac.Equal(got, want) {
		return ErrInvalid
	}
	var outcome error
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var id int64
		var expires float64
		var used sql.NullFloat64
		err := tx.QueryRowContext(ctx, "SELECT id, expires_at, used_at FROM auth_login_links WHERE nonce_hash = ?", digest(nonce)).Scan(&id, &expires, &used)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			outcome = ErrInvalid
			return nil
		case err != nil:
			return err
		case used.Valid:
			outcome = ErrUsed
			return note(ctx, tx, "refused: #%d was already used", id)
		case s.unix() >= expires:
			outcome = ErrExpired
			return note(ctx, tx, "refused: #%d expired", id)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE auth_login_links SET used_at = ? WHERE id = ?", s.unix(), id); err != nil {
			return err
		}
		return note(ctx, tx, "redeemed #%d", id)
	})
	if err != nil {
		return err
	}
	return outcome
}
