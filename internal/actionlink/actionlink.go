package actionlink

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	Approve   = "approve"
	Dismiss   = "dismiss"
	TTL       = 12 * 60 * 60
	RateLimit = 20
)

var (
	ErrInvalid = errors.New("action link is not valid")
	ErrExpired = errors.New("action link expired")
	ErrUsed    = errors.New("action link was already used")
	ErrRate    = errors.New("too many action links in the last hour")
)

type Claims struct {
	ProposalID int64   `json:"p"`
	Action     string  `json:"a"`
	Hash       string  `json:"h"`
	Nonce      string  `json:"n"`
	Expires    float64 `json:"e"`
}

func Key(ctx context.Context, q db.Querier) ([]byte, error) {
	if raw, ok, err := secrets.FileKey(ctx, secrets.ActionLinkKeyFile); err != nil {
		return nil, err
	} else if ok {
		return hex.DecodeString(string(raw))
	}
	store := secrets.NewIn(settings.From(ctx), nil, q, nil)
	raw, err := store.Value(ctx, secrets.ActionLinkKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		raw, err = store.Generate(ctx, secrets.ActionLinkKey)
		if err != nil {
			return nil, err
		}
	}
	return hex.DecodeString(raw)
}

func Rotate(ctx context.Context, q db.Querier) error {
	if _, ok, err := secrets.FileKey(ctx, secrets.ActionLinkKeyFile); err != nil {
		return err
	} else if ok {
		return settings.AppKeyFile{Key: secrets.ActionLinkKey, File: "app_keys." + secrets.ActionLinkKeyFile}
	}
	store := secrets.NewIn(settings.From(ctx), nil, q, nil)
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return err
	}
	ctx = audit.With(ctx, "phone", audit.UI)
	if err := store.Set(ctx, secrets.ActionLinkKey, hex.EncodeToString(buf[:]), "phone"); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM action_links WHERE used_ts IS NULL")
	return err
}

func Outstanding(ctx context.Context, q db.Querier, now float64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE used_ts IS NULL AND expires_ts > ?", now).Scan(&n)
	return n, err
}

func MintPair(ctx context.Context, q db.Querier, proposalID int64, hash string, now float64) (string, string, error) {
	var recent int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM action_links WHERE created_ts > ?", now-3600).Scan(&recent); err != nil {
		return "", "", err
	}
	if recent >= RateLimit {
		return "", "", ErrRate
	}
	key, err := Key(ctx, q)
	if err != nil {
		return "", "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	nonce := hex.EncodeToString(raw[:])
	expires := now + TTL
	if _, err := q.ExecContext(ctx,
		"INSERT INTO action_links (nonce, proposal_id, content_hash, expires_ts, created_ts) VALUES (?, ?, ?, ?, ?)",
		nonce, proposalID, hash, expires, now); err != nil {
		return "", "", err
	}
	approve, err := sign(key, Claims{ProposalID: proposalID, Action: Approve, Hash: hash, Nonce: nonce, Expires: expires})
	if err != nil {
		return "", "", err
	}
	dismiss, err := sign(key, Claims{ProposalID: proposalID, Action: Dismiss, Hash: hash, Nonce: nonce, Expires: expires})
	if err != nil {
		return "", "", err
	}
	return approve, dismiss, nil
}

func Verify(ctx context.Context, q db.Querier, token string, now float64) (Claims, error) {
	key, err := Key(ctx, q)
	if err != nil {
		return Claims{}, err
	}
	claims, err := parse(key, token)
	if err != nil {
		return Claims{}, err
	}
	if claims.Expires <= now {
		return Claims{}, ErrExpired
	}
	var used *float64
	var proposalID int64
	var hash string
	var expires float64
	err = q.QueryRowContext(ctx,
		"SELECT proposal_id, content_hash, expires_ts, used_ts FROM action_links WHERE nonce = ?", claims.Nonce).
		Scan(&proposalID, &hash, &expires, &used)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	if used != nil {
		return Claims{}, ErrUsed
	}
	if proposalID != claims.ProposalID || hash != claims.Hash || expires != claims.Expires || expires <= now {
		return Claims{}, ErrInvalid
	}
	return claims, nil
}

func Redeem(ctx context.Context, q db.Querier, token string, now float64, apply func(context.Context, Claims) error) error {
	claims, err := Verify(ctx, q, token, now)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, "UPDATE action_links SET used_ts = ? WHERE nonce = ? AND used_ts IS NULL", now, claims.Nonce)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUsed
	}
	return apply(ctx, claims)
}

func sign(key []byte, claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parse(key []byte, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalid
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(mac.Sum(nil), sig) != 1 {
		return Claims{}, ErrInvalid
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrInvalid
	}
	if claims.Action != Approve && claims.Action != Dismiss {
		return Claims{}, ErrInvalid
	}
	return claims, nil
}

var ErrRefused = errors.New("the action is not allowed from the phone")
