package runtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.runtime")

var (
	ErrDraining         = errors.New("runtime: the supervisor is draining")
	errUnexpectedReturn = errors.New("the service returned without being stopped")
)

type PanicError struct {
	Value string
	Stack string
}

func (e *PanicError) Error() string { return "panic: " + e.Value }

type Supervisor struct {
	defs     []ServiceDefinition
	mu       sync.Mutex
	started  bool
	draining bool
	cancel   context.CancelFunc
	done     chan struct{}
	states   map[string]*state
}

type Services struct {
	name     string
	state    *state
	cancel   context.CancelCauseFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	closers  []func() error
	canceled bool
	closed   chan struct{}
}

func New(defs []ServiceDefinition) (*Supervisor, error) {
	valid, err := validate(defs)
	if err != nil {
		return nil, err
	}
	s := &Supervisor{defs: valid, done: make(chan struct{}), states: map[string]*state{}}
	for _, def := range valid {
		s.states[def.Name] = &state{name: def.Name, critical: def.Critical, stall: def.Stall, status: StatusStopped}
	}
	return s, nil
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return ErrDraining
	}
	if s.started {
		return nil
	}
	s.started = true
	root, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	var wg sync.WaitGroup
	for _, def := range s.defs {
		st := s.states[def.Name]
		st.set(StatusStarting)
		wg.Go(func() { s.loop(root, def, st) })
	}
	go func() {
		wg.Wait()
		close(s.done)
	}()
	return nil
}

func (s *Supervisor) loop(ctx context.Context, def ServiceDefinition, st *state) {
	attempt := 0
	for {
		started := time.Now()
		log.Info(fmt.Sprintf("service %s started", def.Name))
		err := s.instance(ctx, def, st)
		if ctx.Err() != nil {
			st.set(StatusStopped)
			return
		}
		if time.Since(started) >= def.Backoff.ResetAfter {
			attempt = 0
		}
		delay := backoffDelay(def.Backoff, attempt)
		attempt++
		st.fail(err, time.Now().Add(delay))
		log.Warn(fmt.Sprintf("service %s failed; restarting in %s", def.Name, delay.Round(time.Millisecond)), logx.Err, err.Error())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			st.set(StatusStopped)
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			st.set(StatusStopped)
			return
		}
	}
}

func backoffDelay(b Backoff, attempt int) time.Duration {
	delay := b.Initial
	for range attempt {
		if delay >= b.Maximum/2 {
			delay = b.Maximum
			break
		}
		delay *= 2
	}
	delay = min(delay, b.Maximum)
	half := delay / 2
	return half + rand.N(delay-half+1)
}

func (s *Supervisor) instance(ctx context.Context, def ServiceDefinition, st *state) error {
	inst, cancel := context.WithCancelCause(ctx)
	svc := &Services{name: def.Name, state: st, cancel: cancel, closed: make(chan struct{})}
	stop := context.AfterFunc(inst, svc.close)
	st.begin()
	err := guard(func() error { return def.Run(inst, svc) })
	if err == nil && ctx.Err() == nil {
		err = errUnexpectedReturn
	}
	cancel(err)
	if stop() {
		svc.close()
	} else {
		<-svc.closed
	}
	svc.wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if cause := context.Cause(inst); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	if err == nil {
		err = errUnexpectedReturn
	}
	return err
}

func guard(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &PanicError{Value: fmt.Sprintf("%T", p), Stack: string(debug.Stack())}
		}
	}()
	return fn()
}

func (s *Services) Go(ctx context.Context, name string, fn func(context.Context) error) {
	s.wg.Go(func() {
		err := guard(func() error { return fn(ctx) })
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			var p *PanicError
			if errors.As(err, &p) {
				log.Error(fmt.Sprintf("service %s child %s panicked after cancel", s.name, name), logx.Err, p.Error()+"\n"+p.Stack)
			}
			return
		}
		s.cancel(fmt.Errorf("%s: %w", name, err))
	})
}

func (s *Services) Heartbeat(name string) {
	s.state.heartbeat(name)
}

func (s *Services) OnCancel(fn func() error) {
	s.mu.Lock()
	if !s.canceled {
		s.closers = append(s.closers, fn)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.runCloser(fn)
}

func (s *Services) close() {
	defer close(s.closed)
	s.mu.Lock()
	closers := s.closers
	s.closers, s.canceled = nil, true
	s.mu.Unlock()
	for i := len(closers) - 1; i >= 0; i-- {
		s.runCloser(closers[i])
	}
}

func (s *Services) runCloser(fn func() error) {
	if err := guard(fn); err != nil {
		log.Warn(fmt.Sprintf("service %s could not close a resource", s.name), logx.Err, err.Error())
	}
}
