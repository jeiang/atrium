package settings

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

type Cipher interface {
	Encrypted() bool
}

type Status string

const (
	Unset  Status = "unset"
	Set    Status = "set"
	Pinned Status = "pinned"
)

type NotSecret string

func (e NotSecret) Error() string { return "secrets: " + string(e) + " is not a secret" }

type NotCredential string

func (e NotCredential) Error() string {
	return "secrets: " + string(e) + " is write-only and has no read path"
}

type NotGenerated string

func (e NotGenerated) Error() string { return "secrets: " + string(e) + " is not a generated key" }

type PinOnly string

func (e PinOnly) Error() string {
	return "secrets: " + string(e) + " can only be pinned by the deployment"
}

type EmptyValue string

func (e EmptyValue) Error() string { return "secrets: " + string(e) + " needs a non-empty value" }

type EncryptionRequired string

func (e EncryptionRequired) Error() string {
	return fmt.Sprintf("secrets: %s can only be stored in an encrypted database and this database is plaintext; encrypt the database first", string(e))
}

type Store struct {
	reg    *Registry
	d      *db.DB
	q      db.Querier
	cipher Cipher
}

func NewStore(reg *Registry, d *db.DB, cipher Cipher) *Store {
	return &Store{reg: reg, d: d, q: d.Write, cipher: cipher}
}

func NewStoreIn(reg *Registry, d *db.DB, q db.Querier, cipher Cipher) *Store {
	return &Store{reg: reg, d: d, q: q, cipher: cipher}
}

func (s *Store) InTx(ctx context.Context, fn func(*Store) error) error {
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		scoped := *s
		scoped.q = tx
		return fn(&scoped)
	})
}

func (s *Store) Reading() *Store {
	if s.d == nil {
		return s
	}
	scoped := *s
	scoped.q = s.d.Read
	return &scoped
}

func (s *Store) Known(name string) error {
	_, err := s.def(name)
	return err
}

func (s *Store) def(name string) (Def, error) {
	d, ok := s.reg.Lookup(name)
	if !ok {
		return d, UnknownKey(name)
	}
	if !d.IsSecret() {
		return d, NotSecret(name)
	}
	return d, nil
}

func (s *Store) writable(name string) (Def, error) {
	d, err := s.def(name)
	if err != nil {
		return d, err
	}
	if HostKey(name) {
		return d, HostOwned(name)
	}
	if d.PinnedOnly() {
		return d, PinOnly(name)
	}
	source, pinned, err := s.reg.Pinned(name)
	if err != nil {
		return d, err
	}
	if pinned {
		return d, &PinnedError{Key: name, Source: source}
	}
	return d, nil
}

func (s *Store) Set(ctx context.Context, name, value, by string) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	d, err := s.writable(name)
	if err != nil {
		return err
	}
	if value == "" {
		return EmptyValue(name)
	}
	if d.NeedsEncryptedDB() && (s.cipher == nil || !s.cipher.Encrypted()) {
		return EncryptionRequired(name)
	}
	if _, err = s.q.ExecContext(ctx,
		"INSERT INTO secrets (name, value, set_at, set_by) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET value = excluded.value, set_at = excluded.set_at, set_by = excluded.set_by",
		name, value, now(), by); err != nil {
		return err
	}
	logx.AddSecret(value)
	return audit.Record(ctx, s.q, audit.Entry{Key: name, Secret: true})
}

func (s *Store) Load(ctx context.Context) error {
	rows, err := s.q.QueryContext(ctx, "SELECT value FROM secrets")
	if err != nil {
		return err
	}
	values, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var v string
		return v, r.Scan(&v)
	})
	if err != nil {
		return err
	}
	for _, v := range values {
		logx.AddSecret(v)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, name string) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	d, err := s.def(name)
	if err != nil {
		return err
	}
	if HostKey(name) {
		return HostOwned(name)
	}
	if d.PinnedOnly() {
		return PinOnly(name)
	}
	res, err := s.q.ExecContext(ctx, "DELETE FROM secrets WHERE name = ?", name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	return audit.Record(ctx, s.q, audit.Entry{Key: name, Secret: true})
}

func (s *Store) Status(ctx context.Context, name string) (Status, error) {
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

func (s *Store) Generate(ctx context.Context, name string) (string, error) {
	d, err := s.def(name)
	if err != nil {
		return "", err
	}
	if !d.IsGenerated() {
		return "", NotGenerated(name)
	}
	if _, pinned, err := s.reg.Pinned(name); err != nil {
		return "", err
	} else if pinned {
		found, err := s.reg.Resolve(ctx, s.q, name)
		if err != nil {
			return "", err
		}
		if found.Value == "" {
			return "", EmptyValue(name)
		}
		logx.AddSecret(found.Value)
		return found.Value, nil
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
	if err == nil {
		logx.AddSecret(value)
	}
	return value, err
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (s *Store) Value(ctx context.Context, name string) (string, error) {
	if _, err := s.def(name); err != nil {
		return "", err
	}
	if _, pinned, err := s.reg.Pinned(name); err != nil {
		return "", err
	} else if pinned {
		found, err := s.reg.Resolve(ctx, s.q, name)
		if err != nil {
			return "", err
		}
		if found.Value == "" {
			return "", EmptyValue(name)
		}
		logx.AddSecret(found.Value)
		return found.Value, nil
	}
	var value string
	err := s.q.QueryRowContext(ctx, "SELECT value FROM secrets WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err == nil {
		logx.AddSecret(value)
	}
	return value, err
}

func (s *Store) Credential(ctx context.Context, name string) (string, error) {
	d, err := s.def(name)
	if err != nil {
		return "", err
	}
	if !d.Readable {
		return "", NotCredential(name)
	}
	return s.Value(ctx, name)
}

func (s *Store) Adopt(ctx context.Context, name, value string) (bool, error) {
	d, err := s.def(name)
	if err != nil {
		return false, err
	}
	if !d.IsGenerated() {
		return false, NotGenerated(name)
	}
	if value == "" {
		return false, EmptyValue(name)
	}
	if _, pinned, err := s.reg.Pinned(name); err != nil || pinned {
		return false, err
	}
	res, err := s.q.ExecContext(ctx,
		"INSERT OR IGNORE INTO secrets (name, value, set_at, set_by) VALUES (?, ?, ?, 'migration')",
		name, value, now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		logx.AddSecret(value)
	}
	return n > 0, nil
}

type Snapshot struct {
	settings map[string]string
	secrets  map[string]string
}

func (s *Store) Snapshot(ctx context.Context) (*Snapshot, error) {
	out := &Snapshot{settings: map[string]string{}, secrets: map[string]string{}}
	for _, table := range []struct {
		query string
		into  map[string]string
	}{
		{"SELECT key, value FROM settings", out.settings},
		{"SELECT name, value FROM secrets", out.secrets},
	} {
		rows, err := s.q.QueryContext(ctx, table.query)
		if err != nil {
			return nil, err
		}
		pairs, err := db.Collect(rows, func(r *sql.Rows) (struct{ key, value string }, error) {
			var pair struct{ key, value string }
			err := r.Scan(&pair.key, &pair.value)
			return pair, err
		})
		if err != nil {
			return nil, err
		}
		for _, pair := range pairs {
			table.into[pair.key] = pair.value
		}
	}
	return out, nil
}

func (s *Store) Resolve(ctx context.Context, snap *Snapshot, key string) (Resolved, error) {
	d, pins, err := s.reg.lookup(key)
	if err != nil {
		return Resolved{}, err
	}
	if pins != nil {
		if value, source, ok := pins.Pin(key); ok {
			return Resolved{Key: key, Value: value, Origin: FromPin, PinSource: source}, nil
		}
	}
	out := Resolved{Key: key, Value: d.Default, Origin: FromDefault}
	if d.IsSecret() {
		if value, ok := snap.secrets[key]; ok {
			out.Value, out.Origin = value, FromInstance
		}
		return out, nil
	}
	if value, ok := snap.settings[key]; ok {
		out.Value, out.Origin = value, FromInstance
	}
	return out, nil
}
