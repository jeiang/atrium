package db

import (
	"context"
	"sync"
)

type ReadyHook func(ctx context.Context, d *DB) error

var ready struct {
	mu    sync.RWMutex
	hooks []ReadyHook
}

func OnReady(h ReadyHook) {
	ready.mu.Lock()
	defer ready.mu.Unlock()
	ready.hooks = append(ready.hooks, h)
}

func Ready(ctx context.Context, d *DB) error {
	ready.mu.RLock()
	hooks := append([]ReadyHook(nil), ready.hooks...)
	ready.mu.RUnlock()
	for _, h := range hooks {
		if err := h(ctx, d); err != nil {
			return err
		}
	}
	return nil
}
