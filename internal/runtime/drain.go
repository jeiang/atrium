package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *Supervisor) Drain(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	started := s.started
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
	}
	var running []string
	now := time.Now()
	for _, def := range s.defs {
		if s.states[def.Name].snapshot(now).Status != StatusStopped {
			running = append(running, def.Name)
		}
	}
	return fmt.Errorf("runtime: services still running at the drain deadline: %s: %w", strings.Join(running, ", "), context.Cause(ctx))
}
