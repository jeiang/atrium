package app

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
)

func classifierFor(ctx context.Context, svc *service.Service) (*classifier.Service, error) {
	now := svc.Now
	if now == nil {
		now = assistant.Now
	}
	client, err := svc.ResolveClient(ctx)
	if missing, unknown := new(config.NotConfigured), new(connections.UnknownID); errors.As(err, missing) || errors.As(err, unknown) {
		client, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	return classifier.New(svc.DB, svc.Inference, client, now), nil
}
