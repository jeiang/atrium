package settings

import (
	"context"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

var retiredKeys = [...]struct{ key, replacement string }{
	{"max_run_cost_usd", "max_run_cost_usd_*"},
	{"max_steps", "max_steps_*"},
}

type Orphan struct {
	Key         string
	Value       string
	Replacement string
}

func (r *Registry) retired(key string) (replacement string, ok bool) {
	if _, registered := r.Lookup(key); registered {
		return "", false
	}
	for _, k := range retiredKeys {
		if k.key == key {
			return k.replacement, true
		}
	}
	return "", false
}

func (r *Registry) IsRetired(key string) bool {
	_, ok := r.retired(key)
	return ok
}

func (r *Registry) replacements(pattern string) string {
	prefix, wild := strings.CutSuffix(pattern, "*")
	var names []string
	for _, d := range r.Defs() {
		if d.Key == pattern || wild && strings.HasPrefix(d.Key, prefix) {
			names = append(names, d.Key)
		}
	}
	return strings.Join(names, ", ")
}

func (r *Registry) Orphans(ctx context.Context, q db.Querier) ([]Orphan, error) {
	var out []Orphan
	for _, k := range retiredKeys {
		if !r.IsRetired(k.key) {
			continue
		}
		value, ok, err := instanceValue(ctx, q, k.key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, Orphan{Key: k.key, Value: value, Replacement: r.replacements(k.replacement)})
		}
	}
	return out, nil
}

var ErrNoOrphan = errors.New("settings: no stored value for this retired setting")

func (r *Registry) DropOrphan(ctx context.Context, q db.Querier, key string) error {
	if !r.IsRetired(key) {
		return UnknownKey(key)
	}
	value, ok, err := instanceValue(ctx, q, key)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoOrphan
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key); err != nil {
		return err
	}
	return audit.Record(ctx, q, audit.Entry{Key: key, Old: &value})
}

func tableMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func (r *Registry) OrphansIfPresent(ctx context.Context, q db.Querier) ([]Orphan, error) {
	out, err := r.Orphans(ctx, q)
	if tableMissing(err) {
		return nil, nil
	}
	return out, err
}
