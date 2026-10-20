package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

type moduled struct{}

func (moduled) Error() string    { return "unknown proposal 9" }
func (moduled) PyClass() string  { return "ProposalError" }
func (moduled) PyModule() string { return "atrium.proposals" }

func failing(ctx context.Context, err error) (int, string) {
	var stderr bytes.Buffer
	code := Fail(&Env{Ctx: ctx, Stderr: &stderr}, err)
	return code, stderr.String()
}

func TestFailPrintsTheLastTracebackLine(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("missing credentials for gmail"), "RuntimeError: missing credentials for gmail\n"},
		{moduled{}, "atrium.proposals.ProposalError: unknown proposal 9\n"},
	} {
		if code, stderr := failing(context.Background(), c.err); code != ExitError || stderr != c.want {
			t.Errorf("Fail(%v) = %d, %q; want %d, %q", c.err, code, stderr, ExitError, c.want)
		}
	}
}

func TestSignalsEndAFailureLikePython(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code int
		want string
	}{
		{syscall.SIGINT, ExitInterrupt, "KeyboardInterrupt\n"},
		{syscall.SIGTERM, ExitTerminated, ""},
		{syscall.SIGHUP, ExitHangup, ""},
	} {
		ctx, stop := SignalContext(context.Background())
		if err := syscall.Kill(os.Getpid(), c.sig); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%v did not end the context", c.sig)
		}
		if got, ok := Received(ctx); !ok || got != c.sig {
			t.Errorf("Received = %v, %v; want %v", got, ok, c.sig)
		}
		if code, stderr := failing(ctx, context.Canceled); code != c.code || stderr != c.want {
			t.Errorf("Fail after %v = %d, %q; want %d, %q", c.sig, code, stderr, c.code, c.want)
		}
		stop()
	}
}

func TestACancelledContextWithoutASignalIsAnOrdinaryFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, stderr := failing(ctx, context.Canceled); code != ExitError || stderr != "RuntimeError: context canceled\n" {
		t.Errorf("Fail = %d, %q", code, stderr)
	}
}
