package run

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func app(w *world) *cli.App {
	clients := func(config.Config, config.Env) func(context.Context, string) (mail.Client, error) { return w.open }
	a := cli.New("atrium", nil)
	a.Add(SyncCommand(clients), WatchCommand(clients))
	return a
}

func runCLI(t *testing.T, ctx context.Context, w *world, vars map[string]string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("ATRIUM_DB", w.cfg.DBPath)
	t.Setenv("ATRIUM_CACHE_DIR", w.cfg.CacheDir)
	t.Setenv("ATRIUM_MODE", w.cfg.Mode)
	for _, k := range []string{"ICLOUD_MAIL_USERNAME", "ICLOUD_APP_PASSWORD", "GMAIL_ADDRESS", "GMAIL_APP_PASSWORD"} {
		t.Setenv(k, vars[k])
	}
	var stdout, stderr bytes.Buffer
	code := app(w).Run(ctx, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestSyncCommand(t *testing.T) {
	w := newWorld(t, config.Shadow, nil)
	w.arrive(config.ICloud, "Hello", "a@x.test")
	code, stdout, stderr := runCLI(t, context.Background(), w, credentials, "sync", "--account", "icloud")
	if code != cli.ExitOK || stdout != "" || stderr != "" {
		t.Fatalf("sync = %d, %q, %q", code, stdout, stderr)
	}
	if n := w.count("SELECT COUNT(*) FROM messages"); n != 1 {
		t.Errorf("messages = %d", n)
	}
}

func TestSyncCommandFailsForAnExplicitAccountWithoutCredentials(t *testing.T) {
	w := newWorld(t, config.Shadow, nil)
	code, stdout, stderr := runCLI(t, context.Background(), w, nil, "sync", "--account", "gmail")
	if code != cli.ExitError || stdout != "" || stderr != "RuntimeError: missing credentials for gmail\n" {
		t.Fatalf("sync = %d, %q, %q", code, stdout, stderr)
	}
	if len(w.opens()) != 0 {
		t.Errorf("opened %v", w.opens())
	}
}

func TestSyncAndWatchRejectAnUnknownAccount(t *testing.T) {
	w := newWorld(t, config.Shadow, nil)
	for _, cmd := range []string{"sync", "watch"} {
		code, stdout, stderr := runCLI(t, context.Background(), w, nil, cmd, "--account", "work")
		want := "atrium " + cmd + ": error: argument --account: invalid choice: 'work' (choose from 'gmail', 'icloud')\n"
		if code != cli.ExitUsage || stdout != "" || !strings.HasSuffix(stderr, want) {
			t.Errorf("%s --account work = %d, %q, %q", cmd, code, stdout, stderr)
		}
		code, _, stderr = runCLI(t, context.Background(), w, nil, cmd, "extra")
		if code != cli.ExitUsage || !strings.HasSuffix(stderr, "\natrium: error: unrecognized arguments: extra\n") {
			t.Errorf("%s extra = %d, %q", cmd, code, stderr)
		}
	}
}

func TestWatchCommandExitsZeroOnShutdown(t *testing.T) {
	w := newWorld(t, config.Shadow, nil)
	w.fakes[config.ICloud].Idle = true
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	code, stdout, stderr := runCLI(t, ctx, w, credentials, "watch")
	if code != cli.ExitOK || stdout != "" || stderr != "" || len(w.opens()) == 0 {
		t.Fatalf("watch = %d, %q, %q after opening %v", code, stdout, stderr, w.opens())
	}
	code, _, _ = runCLI(t, context.Background(), w, nil, "watch")
	if code != cli.ExitOK {
		t.Fatalf("watch with no account = %d", code)
	}
}

func TestWatchCommandNotifiesSystemd(t *testing.T) {
	w := newWorld(t, config.Shadow, nil)
	w.fakes[config.ICloud].Idle = true
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		code, _, _ := runCLI(t, ctx, w, credentials, "watch")
		done <- code
	}()
	notify.Wait("READY=1")
	notify.Wait("STATUS=watching")
	notify.Wait("WATCHDOG=1")
	cancel()
	if code := <-done; code != cli.ExitOK {
		t.Fatalf("watch = %d", code)
	}
	notify.Wait("STOPPING=1")
}

func TestSyncInterruptedBySignalEndsLikePython(t *testing.T) {
	for _, c := range []struct {
		sig    syscall.Signal
		code   int
		stderr string
	}{
		{syscall.SIGINT, cli.ExitInterrupt, "KeyboardInterrupt\n"},
		{syscall.SIGTERM, cli.ExitTerminated, ""},
	} {
		w := newWorld(t, config.Shadow, nil)
		ctx, stop := cli.SignalContext(context.Background())
		w.Fail = func(string, int) error {
			if err := syscall.Kill(os.Getpid(), c.sig); err != nil {
				t.Error(err)
			}
			<-ctx.Done()
			return ctx.Err()
		}
		code, stdout, stderr := runCLI(t, ctx, w, credentials, "sync")
		stop()
		if code != c.code || stdout != "" || stderr != c.stderr {
			t.Errorf("sync after %v = %d, %q, %q; want %d, \"\", %q", c.sig, code, stdout, stderr, c.code, c.stderr)
		}
	}
}
