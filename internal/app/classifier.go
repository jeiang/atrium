package app

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
)

func classifierFor(ctx context.Context, svc *service.Service) (*classifier.Service, error) {
	now := svc.Now
	if now == nil {
		now = assistant.Now
	}
	client, err := svc.ResolveClient(ctx)
	if missing := new(config.NotConfigured); errors.As(err, missing) {
		client, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	return classifier.New(svc.DB, svc.Inference, client, now), nil
}
