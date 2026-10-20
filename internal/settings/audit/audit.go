package audit

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

type Source string

const (
	CLI       Source = "cli"
	UI        Source = "ui"
	Migration Source = "migration"
)

const Changed = "changed"

var ErrNoActor = errors.New("audit: the context carries no actor and source")

type origin struct {
	actor  string
	source Source
}

type originKey struct{}

func With(ctx context.Context, actor string, source Source) context.Context {
	return context.WithValue(ctx, originKey{}, origin{actor: actor, source: source})
}

type Entry struct {
	Key    string
	Thread int64
	Old    *string
	New    *string
	Secret bool
}

func Record(ctx context.Context, q db.Querier, e Entry) error {
	o, ok := ctx.Value(originKey{}).(origin)
	if !ok || o.actor == "" || o.source == "" {
		return ErrNoActor
	}
	old, next := scrub(e.Old), scrub(e.New)
	if e.Secret {
		changed := Changed
		old, next = nil, &changed
	}
	var thread any
	if e.Thread != 0 {
		thread = e.Thread
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO settings_audit (ts, key, thread_id, old_value, new_value, secret, actor, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		float64(time.Now().UnixNano())/1e9, e.Key, thread, old, next, e.Secret, o.actor, string(o.source))
	return err
}

func scrub(v *string) *string {
	if v == nil {
		return nil
	}
	s := logx.Scrub(*v)
	return &s
}
