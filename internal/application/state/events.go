package state

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
)

const EventsName application.Name = "state.events"

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
