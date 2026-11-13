package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
			serve := serveCLIProcess(t, t.TempDir())
			if code := serve.stop(t, tc.sig); code != cli.ExitOK {
				t.Fatalf("serve after %s exited with code %d, want 0; stderr %q", tc.sig, code, serve.stderr.String())
			}
			if strings.Contains(serve.stderr.String(), "Traceback") {
				t.Errorf("stderr contains a traceback: %q", serve.stderr.String())
			}
		})
	}
}

type serveProcess struct {
	addr   string
	stderr *lockedBuffer
	cmd    *exec.Cmd
	done   chan error
	waited bool
	err    error
}

func serveCLIProcess(t *testing.T, dir string) *serveProcess {
	t.Helper()
	addrFile := filepath.Join(dir, "serve-addr")
	cmd := cliExitProcessArgs(t, []string{"serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local"})
	cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_DB="+filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR="+filepath.Join(dir, "cache"), "ATRIUM_PUBLIC_HOST=", "ATRIUM_SERVE_ADDR_FILE="+addrFile)
	return startServeProcess(t, cmd, addrFile)
}

func startServeProcess(t *testing.T, cmd *exec.Cmd, addrFile string) *serveProcess {
	t.Helper()
	p := &serveProcess{stderr: &lockedBuffer{}, cmd: cmd, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = &lockedBuffer{}, p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !p.waited {
			_ = cmd.Process.Kill()
		}
		_ = p.wait()
	})
	deadline := time.Now().Add(30 * time.Second)
	for p.addr == "" {
		if raw, err := os.ReadFile(addrFile); err == nil {
			if addr := strings.TrimSpace(string(raw)); addr != "" {
				if _, _, err := net.SplitHostPort(addr); err == nil {
					p.addr = addr
					break
				}
			}
		}
		select {
		case err := <-p.done:
			p.err, p.waited = err, true
			t.Fatalf("serve exited before reporting its address: %v, stderr %q", err, p.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not report its address, stderr %q", p.stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.DialTimeout("tcp", p.addr, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("serve did not accept connections at %s: %v, stderr %q", p.addr, err, p.stderr.String())
	}
	_ = conn.Close()
	return p
}

func (p *serveProcess) wait() error {
	if !p.waited {
		p.err = <-p.done
		p.waited = true
	}
	return p.err
}

func (p *serveProcess) stop(t *testing.T, sig os.Signal) int {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	return exitCode(p.wait())
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

func TestRemovedDaemonCommandsReturnUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"watch"}, {"agent", "scheduler"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			cmd := cliExitProcessArgs(t, args)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if code := exitCode(cmd.Run()); code != cli.ExitUsage {
				t.Fatalf("%v exit = %d, want %d: %s", args, code, cli.ExitUsage, stderr.String())
			}
			help := cliExitProcessArgs(t, append(args[:len(args)-1:len(args)-1], "--help"))
			out, err := help.Output()
			if err != nil {
				t.Fatal(err)
			}
			if regexp.MustCompile(`\b` + args[len(args)-1] + `\b`).Match(out) {
				t.Fatalf("retired command appears in help: %s", out)
			}
		})
	}
}
