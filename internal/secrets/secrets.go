package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AnthropicAdminKey = "anthropic_admin_key"
	OpenAIAdminKey    = "openai_admin_key"
	ActionLinkKey     = "action_link_key"
)

func init() {
	settings.MustRegister(
		settings.Def{Key: AnthropicAdminKey, Module: "anthropic", Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true},
		settings.Def{Key: OpenAIAdminKey, Module: "openai", Type: settings.String, Class: settings.Secret, Secret: true, RequiresEncryptedDB: true},
		settings.Def{Key: ActionLinkKey, Module: "action_links", Type: settings.String, Class: settings.Secret, Secret: true, Generated: true},
	)
}

type Cipher interface {
	Encrypted() bool
}

type Plaintext struct{}

func (Plaintext) Encrypted() bool { return false }

type Status string

const (
	Unset  Status = "unset"
	Set    Status = "set"
	Pinned Status = "pinned"
)

type NotSecret string

func (e NotSecret) Error() string { return "secrets: " + string(e) + " is not a secret" }

type NotGenerated string

func (e NotGenerated) Error() string { return "secrets: " + string(e) + " is not a generated key" }

type EmptyValue string

func (e EmptyValue) Error() string { return "secrets: " + string(e) + " needs a non-empty value" }

type EncryptionRequired string

func (e EncryptionRequired) Error() string {
	return fmt.Sprintf("secrets: %s can only be stored in an encrypted database and this database is plaintext; encrypt the database first", string(e))
}

type Store struct {
	reg    *settings.Registry
	q      db.Querier
	cipher Cipher
}

func New(q db.Querier, cipher Cipher) *Store {
	return &Store{reg: settings.Default, q: q, cipher: cipher}
}

func (s *Store) Known(name string) error {
	_, err := s.def(name)
	return err
}

func (s *Store) def(name string) (settings.Def, error) {
	d, ok := s.reg.Lookup(name)
	if !ok {
		return d, settings.UnknownKey(name)
	}
	if !d.Secret {
		return d, NotSecret(name)
	}
	return d, nil
}

func (s *Store) writable(name string) (settings.Def, error) {
	d, err := s.def(name)
	if err != nil {
		return d, err
	}
	source, pinned, err := s.reg.Pinned(name)
	if err != nil {
		return d, err
	}
	if pinned {
		return d, &settings.PinnedError{Key: name, Source: source}
	}
	return d, nil
}

func (s *Store) Set(ctx context.Context, name, value, by string) error {
	d, err := s.writable(name)
	if err != nil {
		return err
	}
	if value == "" {
		return EmptyValue(name)
	}
	if d.RequiresEncryptedDB && !s.cipher.Encrypted() {
		return EncryptionRequired(name)
	}
	_, err = s.q.ExecContext(ctx,
		"INSERT INTO secrets (name, value, set_at, set_by) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET value = excluded.value, set_at = excluded.set_at, set_by = excluded.set_by",
		name, value, now(), by)
	return err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	if _, err := s.def(name); err != nil {
		return err
	}
	_, err := s.q.ExecContext(ctx, "DELETE FROM secrets WHERE name = ?", name)
	return err
}

func (s *Store) IsSet(ctx context.Context, name string) (Status, error) {
	if _, err := s.def(name); err != nil {
		return "", err
	}
	if _, pinned, err := s.reg.Pinned(name); err != nil || pinned {
		if pinned {
			return Pinned, nil
		}
		return "", err
	}
	var one int
	err := s.q.QueryRowContext(ctx, "SELECT 1 FROM secrets WHERE name = ?", name).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Unset, nil
	case err != nil:
		return "", err
	}
	return Set, nil
}

func (s *Store) GeneratedKey(ctx context.Context, name string) (string, error) {
	d, err := s.def(name)
	if err != nil {
		return "", err
	}
	if !d.Generated {
		return "", NotGenerated(name)
	}
	if _, pinned, err := s.reg.Pinned(name); err != nil {
		return "", err
	} else if pinned {
		found, err := s.reg.Resolve(ctx, s.q, name, 0)
		return found.Value, err
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	if _, err := s.q.ExecContext(ctx,
		"INSERT OR IGNORE INTO secrets (name, value, set_at, set_by) VALUES (?, ?, ?, 'generated')",
		name, hex.EncodeToString(buf[:]), now()); err != nil {
		return "", err
	}
	var value string
	err = s.q.QueryRowContext(ctx, "SELECT value FROM secrets WHERE name = ?", name).Scan(&value)
	return value, err
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }
