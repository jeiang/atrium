package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jeiang/atrium/internal/application"
)

type signalKey struct{}

type received struct {
	mu  sync.Mutex
	sig os.Signal
}

func (r *received) set(sig os.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sig == nil {
		r.sig = sig
	}
}

func (r *received) get() os.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sig
}

func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	state := &received{}
	ctx, cancel := context.WithCancel(context.WithValue(parent, signalKey{}, state))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		select {
		case sig := <-signals:
			state.set(sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}

func Received(ctx context.Context) (os.Signal, bool) {
	state, _ := ctx.Value(signalKey{}).(*received)
	if state == nil {
		return nil, false
	}
	sig := state.get()
	return sig, sig != nil
}

func Fail(env *Env, err error) int {
	if code, ok := canceled(env.Ctx, env.Stderr); ok {
		return code
	}
	var e *application.Error
	if !errors.As(err, &e) {
		printf(env.Stderr, "error: %s\n", err.Error())
		return ExitError
	}
	printf(env.Stderr, "error: %s\n", e.Error())
	for _, f := range e.Fields {
		printf(env.Stderr, "  %s: %s\n", f.Name, f.Message)
	}
	if len(e.EffectIDs) > 0 {
		printf(env.Stderr, "  effects: %v\n", e.EffectIDs)
	}
	if e.Code == application.Invalid {
		return ExitUsage
	}
	return ExitError
}

func canceled(ctx context.Context, stderr io.Writer) (int, bool) {
	if ctx.Err() == nil {
		return 0, false
	}
	if code, ok := signalExit(ctx, stderr); ok {
		return code, true
	}
	return ExitOK, true
}

func signalExit(ctx context.Context, stderr io.Writer) (int, bool) {
	if ctx.Err() == nil {
		return 0, false
	}
	switch sig, _ := Received(ctx); sig {
	case syscall.SIGINT:
		printf(stderr, "interrupted\n")
		return ExitInterrupt, true
	case syscall.SIGTERM:
		return ExitTerminated, true
	case syscall.SIGHUP:
		return ExitHangup, true
	default:
		return 0, false
	}
}
