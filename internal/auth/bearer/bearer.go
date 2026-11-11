package bearer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/db"
)

const (
	Prefix        = "atr_"
	secretBytes   = 32
	maxName       = 100
	maxTokenBytes = 64 << 10
	kindLocal     = "local"
	kindOIDC      = "oidc"
	oidcName      = "OIDC access token"
)

var (
	ErrInvalid  = errors.New("bearer: the token is not valid")
	ErrRevoked  = errors.New("bearer: the token is revoked")
	ErrExpired  = errors.New("bearer: the token expired")
	ErrNotFound = errors.New("bearer: the token does not exist")
	ErrName     = errors.New("bearer: the name is required and at most 100 characters")
	ErrExpiry   = errors.New("bearer: the expiry must be in the future")
)

type Token struct {
	ID         int64
	Kind       string
	Name       string
	Subject    string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type Issued struct {
	Token  Token
	Secret string
}

type Service struct {
	d    *db.DB
	oidc *oidc.Service
	now  func() time.Time
}

func New(d *db.DB, oidc *oidc.Service) *Service {
	return &Service{d: d, oidc: oidc, now: time.Now}
}

func (s *Service) Issue(ctx context.Context, name string, expiresAt *time.Time) (Issued, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return Issued{}, ErrName
	}
	now := s.now()
	var expires *time.Time
	var stored any
	if expiresAt != nil {
		if !expiresAt.After(now) {
			return Issued{}, ErrExpiry
		}
		value := *expiresAt
		expires = &value
		stored = unix(value)
	}
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return Issued{}, err
	}
	secret := Prefix + base64.RawURLEncoding.EncodeToString(raw)
	var tok Token
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO auth_bearers (token_hash, kind, name, created_at, expires_at) VALUES (?, ?, ?, ?, ?)",
			hashSecret(secret), kindLocal, name, unix(now), stored)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		tok = Token{ID: id, Kind: kindLocal, Name: name, CreatedAt: now, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return Issued{}, err
	}
	return Issued{Token: tok, Secret: secret}, nil
}

func (s *Service) List(ctx context.Context) ([]Token, error) {
	rows, err := s.d.Read.QueryContext(ctx,
		"SELECT id, kind, name, subject, created_at, expires_at, last_used_at, revoked_at FROM auth_bearers ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Token, error) {
		var tok Token
		var created float64
		var expires, used, revoked sql.NullFloat64
		err := r.Scan(&tok.ID, &tok.Kind, &tok.Name, &tok.Subject, &created, &expires, &used, &revoked)
		if err != nil {
			return Token{}, err
		}
		tok.CreatedAt = fromUnix(created)
		tok.ExpiresAt = optional(expires)
		tok.LastUsedAt = optional(used)
		tok.RevokedAt = optional(revoked)
		return tok, nil
	})
}

func (s *Service) Revoke(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrNotFound
	}
	now := unix(s.now())
	found := false
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE auth_bearers SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			found = true
			return nil
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_bearers WHERE id = ?", id).Scan(&exists); err != nil {
			return err
		}
		found = exists > 0
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Authenticate(ctx context.Context, raw string) (Token, error) {
	if strings.HasPrefix(raw, Prefix) {
		return s.authenticateLocal(ctx, raw)
	}
	return s.authenticateOIDC(ctx, raw)
}

func (s *Service) authenticateLocal(ctx context.Context, raw string) (Token, error) {
	if !validLocal(raw) {
		return Token{}, ErrInvalid
	}
	var tok Token
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		return s.admit(ctx, tx, hashSecret(raw), s.now(), &tok)
	})
	return tok, err
}

func (s *Service) authenticateOIDC(ctx context.Context, raw string) (Token, error) {
	if s.oidc == nil {
		return Token{}, oidc.ErrAPIOff
	}
	if raw == "" || len(raw) > maxTokenBytes {
		return Token{}, ErrInvalid
	}
	hash := hashSecret(raw)
	revoked, err := s.revoked(ctx, hash)
	if err != nil {
		return Token{}, err
	}
	if revoked {
		return Token{}, ErrRevoked
	}
	session, err := s.oidc.AuthenticateBearer(ctx, raw)
	if err != nil {
		return Token{}, err
	}
	expires, ok := oidc.EffectiveExpiry(raw)
	if !ok {
		return Token{}, ErrInvalid
	}
	now := s.now()
	var tok Token
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO auth_bearers (token_hash, kind, name, subject, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(token_hash) DO NOTHING",
			hash, kindOIDC, oidcName, session.Subject, unix(now), unix(expires))
		if err != nil {
			return err
		}
		return s.admit(ctx, tx, hash, now, &tok)
	})
	return tok, err
}

func (s *Service) admit(ctx context.Context, q db.Querier, hash string, now time.Time, out *Token) error {
	tok, err := load(ctx, q, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	if tok.RevokedAt != nil {
		return ErrRevoked
	}
	if tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt) {
		return ErrExpired
	}
	if _, err := q.ExecContext(ctx, "UPDATE auth_bearers SET last_used_at = ? WHERE id = ?", unix(now), tok.ID); err != nil {
		return err
	}
	*out = tok
	return nil
}

func (s *Service) revoked(ctx context.Context, hash string) (bool, error) {
	var at sql.NullFloat64
	err := s.d.Read.QueryRowContext(ctx, "SELECT revoked_at FROM auth_bearers WHERE token_hash = ?", hash).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return at.Valid, nil
}

func load(ctx context.Context, q db.Querier, hash string) (Token, error) {
	var tok Token
	var created float64
	var expires, used, revoked sql.NullFloat64
	err := q.QueryRowContext(ctx,
		"SELECT id, kind, name, subject, created_at, expires_at, last_used_at, revoked_at FROM auth_bearers WHERE token_hash = ?", hash).
		Scan(&tok.ID, &tok.Kind, &tok.Name, &tok.Subject, &created, &expires, &used, &revoked)
	if err != nil {
		return Token{}, err
	}
	tok.CreatedAt = fromUnix(created)
	tok.ExpiresAt = optional(expires)
	tok.LastUsedAt = optional(used)
	tok.RevokedAt = optional(revoked)
	return tok, nil
}

func validLocal(raw string) bool {
	if !strings.HasPrefix(raw, Prefix) {
		return false
	}
	body := raw[len(Prefix):]
	if len(body) != base64.RawURLEncoding.EncodedLen(secretBytes) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(body)
	return err == nil && len(decoded) == secretBytes
}

func hashSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func unix(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

func fromUnix(v float64) time.Time {
	return time.Unix(0, int64(v*1e9)).UTC()
}

func optional(v sql.NullFloat64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromUnix(v.Float64)
	return &t
}
