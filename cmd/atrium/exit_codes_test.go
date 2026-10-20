package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

func TestCLIExitProcess(t *testing.T) {
	if os.Getenv("ATRIUM_CLI_EXIT_E2E") != "1" {
		return
	}
	args := strings.Fields(os.Getenv("ATRIUM_CLI_EXIT_ARGS"))
	ctx, stop := cli.SignalContext(context.Background())
	code := newApp().Run(ctx, args, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func TestCLIExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       string
		wantCode   int
		wantStderr string
	}{
		{name: "usage", wantCode: cli.ExitUsage, wantStderr: "atrium: error: the following arguments are required: command\n"},
		{name: "runtime", args: "codemode-worker", wantCode: cli.ExitError, wantStderr: codemodeRemoved + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := cliExitProcess(t, tc.args)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.wantCode || stdout.Len() != 0 || !strings.HasSuffix(stderr.String(), tc.wantStderr) {
				t.Errorf("CLI exited %d, stdout %q, stderr %q; want %d, empty stdout and suffix %q", code, stdout.String(), stderr.String(), tc.wantCode, tc.wantStderr)
			}
			if tc.name == "usage" && !strings.Contains(stderr.String(), "usage: atrium [-h]") {
				t.Errorf("usage error did not print the CLI usage: %q", stderr.String())
			}
			if strings.Contains(stderr.String(), "Traceback") {
				t.Errorf("stderr contains a traceback: %q", stderr.String())
			}
		})
	}
}

func TestCLISignalExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "interrupt", sig: syscall.SIGINT},
		{name: "terminate", sig: syscall.SIGTERM},
		{name: "hangup", sig: syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
			t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
			t.Setenv("ATRIUM_PUBLIC_HOST", "")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := cliExitProcess(t, "serve --host 127.0.0.1 --port "+port+" --auth local")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			address := net.JoinHostPort("127.0.0.1", port)
			deadline := time.Now().Add(30 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("serve did not start: %v, stderr %q", err, stderr.String())
				}
				if cmd.ProcessState != nil {
					t.Fatalf("serve exited early: %v, stderr %q", cmd.ProcessState, stderr.String())
				}
				time.Sleep(25 * time.Millisecond)
			}
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if code := exitCode(err); code != cli.ExitOK {
				t.Fatalf("serve after %s exited with %v (code %d), want 0; stderr %q", tc.sig, err, code, stderr.String())
			}
			if strings.Contains(stderr.String(), "Traceback") {
				t.Errorf("stderr contains a traceback: %q", stderr.String())
			}
		})
	}
}

func cliExitProcess(t *testing.T, args string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIExitProcess$")
	cmd.Env = append(os.Environ(), "ATRIUM_CLI_EXIT_E2E=1", "ATRIUM_CLI_EXIT_ARGS="+args)
	return cmd
}

func exitCode(err error) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return 0
}
