package app

import (
	"context"
	"errors"
	"sync"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
)

type classifiers struct {
	mu  sync.Mutex
	svc *service.Service
	c   *classifier.Service
}

func (r *classifiers) For(ctx context.Context, svc *service.Service) (*classifier.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil && r.svc == svc {
		return r.c, nil
	}
	loc, err := config.From(ctx).Location(svc.Env)
	if err != nil {
		return nil, err
	}
	now := svc.Now
	if now == nil {
		now = assistant.Now
	}
	client, err := svc.ResolveClient(ctx)
	if missing := new(config.NotConfigured); errors.As(err, missing) {
		return classifier.New(svc.DB, svc.Inference, nil, now, loc), nil
	}
	if err != nil {
		return nil, err
	}
	r.svc, r.c = svc, classifier.New(svc.DB, svc.Inference, client, now, loc)
	return r.c, nil
}
