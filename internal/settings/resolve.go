package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

type Origin string

const (
	FromPin      Origin = "pin"
	FromInstance Origin = "instance"
	FromDefault  Origin = "default"
)

type Resolved struct {
	Key       string
	Value     string
	Origin    Origin
	PinSource string
}

func (r Resolved) Pinned() bool { return r.Origin == FromPin }

func (r Resolved) Float() (float64, error) { return parseFloat(r.Value) }

func (r Resolved) Int() (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(r.Value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %s", strconv.Quote(r.Value))
	}
	return n, nil
}

func (r *Registry) Resolve(ctx context.Context, q db.Querier, key string) (Resolved, error) {
	d, pins, err := r.lookup(key)
	if err != nil {
		return Resolved{}, err
	}
	if pins != nil {
		if value, source, ok := pins.Pin(key); ok {
			return Resolved{Key: key, Value: value, Origin: FromPin, PinSource: source}, nil
		}
	}
	out := Resolved{Key: key, Value: d.Default, Origin: FromDefault}
	if d.IsSecret() || q == nil || !DBBacked(key) {
		return out, nil
	}
	value, ok, err := instanceValue(ctx, q, key)
	if err != nil {
		return Resolved{}, err
	}
	if ok {
		out.Value, out.Origin = value, FromInstance
	}
	return out, nil
}

func (r *Registry) Pinned(key string) (source string, pinned bool, err error) {
	_, pins, err := r.lookup(key)
	if err != nil || pins == nil {
		return "", false, err
	}
	_, source, pinned = pins.Pin(key)
	return source, pinned, nil
}

func instanceValue(ctx context.Context, q db.Querier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (r *Registry) writable(key string) (Def, error) {
	d, pins, err := r.lookup(key)
	if err != nil {
		return Def{}, err
	}
	if HostOwnedKey(key) {
		return Def{}, hostOwned(key)
	}
	if pins != nil {
		if _, source, ok := pins.Pin(key); ok {
			return Def{}, &PinnedError{Key: key, Source: source}
		}
	}
	if !d.Editable() {
		return Def{}, &NotEditable{Key: key}
	}
	return d, nil
}

func (r *Registry) stored(ctx context.Context, q db.Querier, d Def) (*string, error) {
	value, ok, err := instanceValue(ctx, q, d.Key)
	if err != nil || !ok {
		return nil, err
	}
	return &value, nil
}

func (r *Registry) record(ctx context.Context, q db.Querier, key string, old, next *string) error {
	if old == nil && next == nil || old != nil && next != nil && *old == *next {
		return nil
	}
	return audit.Record(ctx, q, audit.Entry{Key: key, Old: old, New: next})
}

func (r *Registry) Set(ctx context.Context, q db.Querier, key, value string) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	d, err := r.writable(key)
	if err != nil {
		return err
	}
	if err := r.Validate(key, value); err != nil {
		return err
	}
	if err := r.checkPeers(ctx, q, d, value); err != nil {
		return err
	}
	old, err := r.stored(ctx, q, d)
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value); err != nil {
		return err
	}
	return r.record(ctx, q, key, old, &value)
}

func (r *Registry) Unset(ctx context.Context, q db.Querier, key string) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	d, err := r.writable(key)
	if err != nil {
		return err
	}
	if d.Default != "" {
		if err := r.checkPeers(ctx, q, d, d.Default); err != nil {
			return err
		}
	}
	old, err := r.stored(ctx, q, d)
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key); err != nil {
		return err
	}
	return r.record(ctx, q, key, old, nil)
}

func (r *Registry) checkPeers(ctx context.Context, q db.Querier, d Def, value string) error {
	if d.Peers == nil {
		return nil
	}
	peer := func(key string) (string, error) {
		found, err := r.Resolve(ctx, q, key)
		return found.Value, err
	}
	if err := d.Peers(value, peer); err != nil {
		var invalid Invalid
		if errors.As(err, &invalid) {
			return Invalid(d.Key + " " + invalid.Error())
		}
		return err
	}
	return nil
}

func (r *Registry) Seed(ctx context.Context, q db.Querier, key, value string) (bool, error) {
	d, _, err := r.lookup(key)
	if err != nil {
		return false, err
	}
	if HostOwnedKey(key) {
		return false, hostOwned(key)
	}
	if !d.Editable() {
		return false, &NotEditable{Key: key}
	}
	if err := d.validate(value); err != nil {
		return false, Invalid(key + " " + err.Error())
	}
	if _, pinned, err := r.Pinned(key); err != nil || pinned {
		return false, err
	}
	res, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)", key, value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	ctx = audit.With(ctx, "bootstrap", audit.Migration)
	return true, r.record(ctx, q, key, nil, &value)
}
