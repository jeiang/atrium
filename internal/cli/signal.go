package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jeiang/atrium/internal/py"
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

// SignalContext is a context that ends at the first SIGINT or SIGTERM; Received tells which one. The returned
// function releases the signal handler.
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

// Received is the signal that ended a context made by SignalContext.
func Received(ctx context.Context) (os.Signal, bool) {
	state, _ := ctx.Value(signalKey{}).(*received)
	if state == nil {
		return nil, false
	}
	sig := state.get()
	return sig, sig != nil
}

// Fail reports err the way an uncaught Python exception ends the process: the last line of its traceback on
// stderr and exit status 1. A failure after SIGINT is Python's KeyboardInterrupt (exit status 130); after SIGTERM
// the process Python would have been killed by it (128+15), with no message.
func Fail(env *Env, err error) int {
	if code, ok := signalExit(env.Ctx, env.Stderr); ok {
		return code
	}
	printf(env.Stderr, "%s\n", py.UncaughtText(err))
	return ExitError
}

func signalExit(ctx context.Context, stderr io.Writer) (int, bool) {
	if ctx.Err() == nil {
		return 0, false
	}
	switch sig, _ := Received(ctx); sig {
	case syscall.SIGINT:
		printf(stderr, "KeyboardInterrupt\n")
		return ExitInterrupt, true
	case syscall.SIGTERM:
		return ExitTerminated, true
	case syscall.SIGHUP:
		return ExitHangup, true
	default:
		return 0, false
	}
}
