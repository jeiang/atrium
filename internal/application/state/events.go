package state

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/application"
)

const EventsName application.Name = "state.events"

var (
	listeners atomic.Int64
	listened  = make(chan struct{}, 1)
)

func Listen() (release func()) {
	listeners.Add(1)
	select {
	case listened <- struct{}{}:
	default:
	}
	return func() { listeners.Add(-1) }
}

func Listening() bool { return listeners.Load() > 0 }

func Listened() <-chan struct{} { return listened }

type EventsInput struct{}

type AppEvent struct {
	Topic    string `json:"topic"`
	Revision int64  `json:"revision"`
}

type AppSync struct {
	Topics []AppEvent `json:"topics"`
}

func DescribeEvents() application.Definition {
	return application.Describe[EventsInput, AppEvent](EventsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Store) All(ctx context.Context) ([]Counter, error) {
	return s.Read(ctx, nil, scopes)
}

func (s *Store) Touch(ctx context.Context, scope string) error {
	if !slices.Contains(scopes, scope) {
		return fmt.Errorf("state: unknown revision scope %q", scope)
	}
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO read_revisions(scope,value) VALUES(?,1) ON CONFLICT(scope) DO UPDATE SET value=value+1", scope)
		return err
	})
}
