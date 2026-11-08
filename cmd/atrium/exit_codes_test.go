package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	if os.Getenv("ATRIUM_CLI_EXIT_E2E") == "1" {
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("ATRIUM_CLI_EXIT_ARGS")), &args); err != nil {
			os.Exit(cli.ExitError)
		}
		ctx, stop := cli.SignalContext(context.Background())
		code := defaultApp(t).Run(ctx, args, os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	cmd := cliExitProcessArgs(t, []string{"--version"})
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--version exited with %v; stderr %q", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("--version printed nothing")
	}
}

func TestCLISignalExitCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "interrupt", sig: syscall.SIGINT},
		{name: "terminate", sig: syscall.SIGTERM},
		{name: "hangup", sig: syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			addrFile := filepath.Join(dir, "serve-addr")
			cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
			cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_DB="+filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR="+filepath.Join(dir, "cache"), "ATRIUM_PUBLIC_HOST=", "ATRIUM_SERVE_ADDR_FILE="+addrFile)
			var stdout, stderr lockedBuffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var waitErr error
			waited := false
			wait := func() error {
				if !waited {
					waitErr = <-done
					waited = true
				}
				return waitErr
			}
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
				}
				_ = wait()
			})
			address := ""
			deadline := time.Now().Add(30 * time.Second)
			for address == "" {
				if raw, err := os.ReadFile(addrFile); err == nil {
					if addr := strings.TrimSpace(string(raw)); addr != "" {
						if _, _, err := net.SplitHostPort(addr); err == nil {
							address = addr
						}
					}
				}
				if address != "" {
					break
				}
				select {
				case err := <-done:
					waitErr, waited = err, true
					t.Fatalf("serve exited before reporting its address: %v, stderr %q", err, stderr.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("serve did not report its address, stderr %q", stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err != nil {
				t.Fatalf("serve did not accept connections at %s: %v, stderr %q", address, err, stderr.String())
			}
			_ = conn.Close()
			if err := cmd.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			err = wait()
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
	return cliExitProcessArgs(t, strings.Fields(args))
}

func cliExitProcessArgs(t *testing.T, args []string) *exec.Cmd {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIExitProcess$")
	cmd.Env = append(os.Environ(), "ATRIUM_CLI_EXIT_E2E=1", "ATRIUM_CLI_EXIT_ARGS="+string(encoded))
	var inherited map[string]string
	if f := fixtureFor(t); f != nil {
		inherited = f.env
	} else {
		inherited = varsFor(t).snapshot()
	}
	for name, value := range inherited {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return cmd
}

func exitCode(err error) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return 0
}

func envWithout(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}
