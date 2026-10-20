package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Origin string

const (
	FromPin      Origin = "pin"
	FromThread   Origin = "thread"
	FromInstance Origin = "instance"
	FromDefault  Origin = "default"
)

type Resolved struct {
	Key       string
	Value     string
	Origin    Origin
	PinSource string
	Secret    bool
}

func (r Resolved) Pinned() bool { return r.Origin == FromPin }

func (r Resolved) Float() (float64, error) { return py.ParseFloat(r.Value) }

func (r Resolved) Int() (int64, error) {
	n, ok := py.ParseInt(r.Value)
	if !ok {
		return 0, fmt.Errorf("invalid literal for int() with base 10: %s", py.Repr(r.Value))
	}
	return n, nil
}

func (r *Registry) Resolve(ctx context.Context, q db.Querier, key string, thread int64) (Resolved, error) {
	d, pins, err := r.lookup(key)
	if err != nil {
		return Resolved{}, err
	}
	if thread != 0 && !d.has(Thread) {
		return Resolved{}, &ScopeError{Key: key, Scope: Thread}
	}
	if pins != nil && d.pinnable() {
		if value, source, ok := pins.Pin(key); ok {
			return Resolved{Key: key, Value: value, Origin: FromPin, PinSource: source, Secret: d.Secret}, nil
		}
	}
	out := Resolved{Key: key, Value: d.Default, Origin: FromDefault, Secret: d.Secret}
	if d.has(Instance) {
		value, ok, err := instanceValue(ctx, q, key)
		if err != nil {
			return Resolved{}, err
		}
		if ok {
			out.Value, out.Origin = value, FromInstance
		}
	}
	if thread == 0 {
		return out, nil
	}
	value, ok, err := threadValue(ctx, q, d.ThreadColumn, thread)
	if err != nil || !ok {
		return out, err
	}
	if d.Class == Cap && out.Origin == FromInstance {
		tighter, err := lower(key, value, out.Value)
		if err != nil {
			return Resolved{}, err
		}
		if !tighter {
			return out, nil
		}
	}
	out.Value, out.Origin = value, FromThread
	return out, nil
}

func (r *Registry) Pinned(key string) (source string, pinned bool, err error) {
	d, pins, err := r.lookup(key)
	if err != nil || pins == nil || !d.pinnable() {
		return "", false, err
	}
	_, source, pinned = pins.Pin(key)
	return source, pinned, nil
}

func lower(key, candidate, base string) (bool, error) {
	c, err := py.ParseFloat(candidate)
	if err != nil {
		return false, fmt.Errorf("settings: %s: %w", key, err)
	}
	b, err := py.ParseFloat(base)
	if err != nil {
		return false, fmt.Errorf("settings: %s: %w", key, err)
	}
	return c < b, nil
}

func instanceValue(ctx context.Context, q db.Querier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func threadValue(ctx context.Context, q db.Querier, column string, thread int64) (string, bool, error) {
	var value sql.NullString
	err := q.QueryRowContext(ctx, "SELECT "+column+" FROM threads WHERE id = ?", thread).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, UnknownThread(thread)
	}
	return value.String, value.Valid, err
}

func (r *Registry) writable(key string, thread int64) (Def, Scope, error) {
	d, pins, err := r.lookup(key)
	if err != nil {
		return Def{}, "", err
	}
	if pins != nil && d.pinnable() {
		if _, source, ok := pins.Pin(key); ok {
			return Def{}, "", &PinnedError{Key: key, Source: source}
		}
	}
	level := Instance
	if thread != 0 {
		level = Thread
	}
	if !d.has(level) {
		return Def{}, "", &ScopeError{Key: key, Scope: level}
	}
	return d, level, nil
}

func (r *Registry) Set(ctx context.Context, q db.Querier, key, value string, thread int64) error {
	d, level, err := r.writable(key, thread)
	if err != nil {
		return err
	}
	if err := r.Validate(key, value); err != nil {
		return err
	}
	if level == Instance {
		_, err = q.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
		return err
	}
	return updateThread(ctx, q, d.ThreadColumn, thread, value)
}

func (r *Registry) Unset(ctx context.Context, q db.Querier, key string, thread int64) error {
	d, level, err := r.writable(key, thread)
	if err != nil {
		return err
	}
	if level == Instance {
		_, err = q.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
		return err
	}
	return updateThread(ctx, q, d.ThreadColumn, thread, nil)
}

func updateThread(ctx context.Context, q db.Querier, column string, thread int64, value any) error {
	res, err := q.ExecContext(ctx, "UPDATE threads SET "+column+" = ? WHERE id = ?", value, thread)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return UnknownThread(thread)
	}
	return nil
}
