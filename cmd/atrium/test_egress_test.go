package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
)

const (
	containmentEnv = "ATRIUM_E2E_CONTAINMENT"
	builtBinaryEnv = "ATRIUM_E2E_BINARY"
	egressProbeEnv = "ATRIUM_EGRESS_PROBE"
	egressDepthEnv = "ATRIUM_EGRESS_PROBE_DEPTH"
)

var publicTargets = []string{"192.0.2.1:80", "[2001:db8::1]:80"}

type containment struct {
	kind  string
	errno syscall.Errno
}

func requireContainment(t *testing.T) containment {
	t.Helper()
	switch kind := os.Getenv(containmentEnv); kind {
	case "linux-netns":
		return containment{kind, syscall.ENETUNREACH}
	case "darwin-sandbox":
		return containment{kind, syscall.EPERM}
	case "":
		t.Skip("loopback containment needs the tools/e2e-loopback runner")
	default:
		t.Fatalf("unknown %s %q", containmentEnv, kind)
	}
	return containment{}
}

func (c containment) refused(t *testing.T, network, addr string) {
	t.Helper()
	start := time.Now()
	conn, err := net.DialTimeout(network, addr, 2*time.Second)
	if err == nil {
		_, err = conn.Write([]byte("x"))
		_ = conn.Close()
	}
	if !errors.Is(err, c.errno) {
		t.Errorf("%s %s under %s: %v, want %v", network, addr, c.kind, err, c.errno)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("%s %s took %s, want an immediate local refusal", network, addr, elapsed)
	}
}

func loopbackListener(t *testing.T, network, addr string) string {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

func TestEgressRunnerHasOnlyLoopback(t *testing.T) {
	c := requireContainment(t)
	for _, addr := range []string{loopbackListener(t, "tcp4", "127.0.0.1:0"), loopbackListener(t, "tcp6", "[::1]:0")} {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("loopback %s: %v", addr, err)
		}
		_ = conn.Close()
	}
	for _, addr := range publicTargets {
		c.refused(t, "tcp", addr)
	}
	c.refused(t, "udp4", "192.0.2.1:53")
	c.refused(t, "udp6", "[2001:db8::1]:53")
	if c.kind != "linux-netns" {
		return
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback == 0 {
			t.Errorf("interface %s is up inside the namespace", iface.Name)
		}
	}
}

func TestEgressDescendantsInheritContainment(t *testing.T) {
	if addr := os.Getenv(egressProbeEnv); addr != "" {
		os.Exit(egressProbe(addr))
	}
	c := requireContainment(t)
	loopback := loopbackListener(t, "tcp", "127.0.0.1:0")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEgressDescendantsInheritContainment$")
	cmd.Env = []string{egressProbeEnv + "=" + loopback, egressDepthEnv + "=1", "TMPDIR=" + t.TempDir()}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe: %v, stderr %q", err, stderr.String())
	}
	want := []string{
		"depth 0 loopback " + loopback + " connected",
		"depth 1 loopback " + loopback + " connected",
	}
	for _, depth := range []string{"0", "1"} {
		for _, addr := range publicTargets {
			want = append(want, "depth "+depth+" public "+addr+" "+strconv.Itoa(int(c.errno)))
		}
	}
	got := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range want {
		if !slices.Contains(got, line) {
			t.Errorf("probe output %q lacks %q", got, line)
		}
	}
}

func egressProbe(loopback string) int {
	depth, err := strconv.Atoi(os.Getenv(egressDepthEnv))
	if err != nil {
		return cli.ExitError
	}
	if depth > 0 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEgressDescendantsInheritContainment$")
		cmd.Env = []string{egressProbeEnv + "=" + loopback, egressDepthEnv + "=" + strconv.Itoa(depth-1), "TMPDIR=" + os.Getenv("TMPDIR")}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return cli.ExitError
		}
	}
	report := func(kind, addr string) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			fmt.Printf("depth %d %s %s connected\n", depth, kind, addr)
			return
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			fmt.Printf("depth %d %s %s %d\n", depth, kind, addr, int(errno))
			return
		}
		fmt.Printf("depth %d %s %s %v\n", depth, kind, addr, err)
	}
	report("loopback", loopback)
	for _, addr := range publicTargets {
		report("public", addr)
	}
	return cli.ExitOK
}

type egressRun struct {
	code           int
	stdout, stderr string
	elapsed        time.Duration
}

func builtAtrium(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	path := os.Getenv(builtBinaryEnv)
	if path == "" {
		t.Fatalf("%s must name the atrium binary the runner built", builtBinaryEnv)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	for name, value := range varsFor(t).snapshot() {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	return cmd
}

func TestEgressCLIWorkflowsReachOnlyLoopback(t *testing.T) {
	requireContainment(t)
	var hits atomic.Int32
	ship24 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/public/v1/trackers" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"trackers":[]}}`))
	}))
	t.Cleanup(ship24.Close)
	var redirects atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, "http://192.0.2.1"+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	const unreachable = "service/ship24\tfailing\tShip24 is unreachable\n"
	bound := time.Duration(tracking.TuningMailShip24TimeoutSeconds) * time.Second / 3

	for _, built := range []bool{false, true} {
		name := "self-exec"
		if built {
			name = "built"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			seedAllFlagsIn(t, dir)
			test := func(url string) egressRun {
				t.Helper()
				setEnv(t, "ATRIUM_HOST_CONFIG", ship24Host(t, dir, url+"/public/v1", "good"))
				args := []string{"connections", "test", "service/ship24"}
				var cmd *exec.Cmd
				if built {
					cmd = builtAtrium(t, args...)
				} else {
					cmd = cliExitProcessArgs(t, args)
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				start := time.Now()
				code := exitCode(cmd.Run())
				return egressRun{code, stdout.String(), stderr.String(), time.Since(start)}
			}
			refused := func(what string, got egressRun) {
				t.Helper()
				if got.code != cli.ExitError || got.stdout != unreachable || got.elapsed >= bound {
					t.Errorf("%s: exit %d after %s, stdout %q, stderr %q; want exit %d with %q within %s", what, got.code, got.elapsed, got.stdout, got.stderr, cli.ExitError, unreachable, bound)
				}
			}
			before := hits.Load()
			if got := test(ship24.URL); got.code != cli.ExitOK || hits.Load() == before {
				t.Fatalf("local fake: exit %d, stdout %q, stderr %q, %d requests", got.code, got.stdout, got.stderr, hits.Load()-before)
			}
			before = hits.Load() + redirects.Load()
			for _, target := range []string{"http://192.0.2.1", "http://[2001:db8::1]", "http://example.com"} {
				refused(target, test(target))
			}
			if after := hits.Load() + redirects.Load(); after != before {
				t.Errorf("public targets reached the local fakes %d times", after-before)
			}
			before = redirects.Load()
			refused("redirect to 192.0.2.1", test(redirect.URL))
			if redirects.Load() == before {
				t.Error("the redirecting local fake was not asked")
			}
		})
	}
}

func TestEgressBuiltServeListensOnLoopback(t *testing.T) {
	requireContainment(t)
	dir := t.TempDir()
	addrFile := filepath.Join(dir, "serve-addr")
	cmd := builtAtrium(t, "serve", "--host", "127.0.0.1", "--port", "0", "--auth", "local")
	cmd.Env = append(envWithout(cmd.Env, "ATRIUM_SERVE_ADDR_FILE="), "ATRIUM_DB="+filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR="+filepath.Join(dir, "cache"), "ATRIUM_PUBLIC_HOST=", "ATRIUM_SERVE_ADDR_FILE="+addrFile)
	serve := startServeProcess(t, cmd, addrFile)
	resp, err := http.Get("http://" + serve.addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	if code := serve.stop(t, syscall.SIGTERM); code != cli.ExitOK {
		t.Errorf("serve after SIGTERM exited %d; stderr %q", code, serve.stderr.String())
	}
}
