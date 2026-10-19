package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuf) From(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()[n:]
}

func pattern(route string) string {
	parts := strings.Split(route, "/")
	for i, p := range parts {
		if p != "" && strings.Trim(p, "0123456789") == "" {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

var cliChecks = [][]string{
	{"live", "status"},
	{"live", "actions"},
	{"proposals", "list"},
	{"shadow-report"},
	{"rules", "export"},
}

var staticRoutes = []string{
	"/healthz",
	"/triage",
	"/rules",
	"/rules/new",
	"/proposals",
	"/activity",
	"/drafts",
	"/loops",
	"/watches",
	"/settings/notifications",
	"/chat",
	"/chat?view=archived",
	"/chat?q=a",
	"/projects",
	"/projects/new",
	"/scheduled",
	"/scheduled/new",
	"/settings",
	"/settings/assistant",
	"/settings/usage",
	"/settings/skills",
	"/settings/subagents",
}

type e2e struct {
	bin    string
	env    []string
	base   string
	routes []string
	srv    *exec.Cmd
	log    *syncBuf
	client *http.Client
}

func newE2E(ctx context.Context, bin, dbPath, cacheDir, contactsDir string, d *db.DB) (*e2e, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("atrium binary %q: %w (build it with: go build -o /tmp/atrium ./cmd/atrium)", bin, err)
	}
	env := append(os.Environ(), "ATRIUM_DB="+dbPath)
	if cacheDir != "" {
		env = append(env, "ATRIUM_CACHE_DIR="+cacheDir)
	}
	if contactsDir != "" {
		env = append(env, "ATRIUM_CONTACTS_DIR="+contactsDir)
	}
	routes, err := discoverRoutes(ctx, d)
	if err != nil {
		return nil, err
	}
	return &e2e{
		bin: path, env: env, routes: routes, log: &syncBuf{},
		client: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func discoverRoutes(ctx context.Context, d *db.DB) ([]string, error) {
	routes := append([]string{}, staticRoutes...)
	for _, q := range []struct {
		query   string
		formats []string
	}{
		{"SELECT id FROM threads ORDER BY updated_at DESC, id DESC LIMIT 20", []string{"/chat/%s", "/chat/%s/work"}},
		{"SELECT id FROM projects ORDER BY id", []string{"/projects/%s", "/projects/%s/edit"}},
		{"SELECT id FROM messages ORDER BY id DESC LIMIT 5", []string{"/messages/%s", "/messages/%s/correct"}},
		{"SELECT id FROM drafts ORDER BY id DESC LIMIT 5", []string{"/drafts/%s"}},
		{"SELECT id FROM jobs ORDER BY id", []string{"/scheduled/%s/edit"}},
		{"SELECT id FROM rules ORDER BY id DESC LIMIT 5", []string{"/rules/%s/edit"}},
	} {
		rows, err := d.Read.QueryContext(ctx, q.query)
		if err != nil {
			return nil, err
		}
		ids, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			var id string
			err := r.Scan(&id)
			return id, err
		})
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			for _, f := range q.formats {
				routes = append(routes, fmt.Sprintf(f, url.PathEscape(id)))
			}
		}
	}
	return routes, nil
}

func (e *e2e) start(ctx context.Context) error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return err
	}
	e.base = "http://127.0.0.1:" + strconv.Itoa(port)
	e.srv = exec.Command(e.bin, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--auth", "local")
	e.srv.Env = e.env
	e.srv.Stdout, e.srv.Stderr = e.log, e.log
	if err := e.srv.Start(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, err := e.get(ctx, "/healthz"); err == nil && status == http.StatusOK {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("atrium serve did not answer /healthz in 30s: %s", strings.TrimSpace(e.log.From(0)))
}

func (e *e2e) stop() {
	if e.srv == nil || e.srv.Process == nil {
		return
	}
	_ = e.srv.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = e.srv.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = e.srv.Process.Kill()
		<-done
	}
}

func (e *e2e) get(ctx context.Context, route string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+route, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, string(body), err
}

func (e *e2e) cli(ctx context.Context, args []string) (int, string) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Env = e.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return code, errb.String()
}

func (e *e2e) checkRoutes(ctx context.Context, r *report) {
	var skipped []string
	for _, route := range e.routes {
		mark := e.log.Len()
		status, _, err := e.get(ctx, route)
		panicked := strings.Contains(e.log.From(mark), "panic")
		switch {
		case err != nil:
			r.fail("GET "+route, err.Error())
		case status >= 500 || panicked:
			r.fail("GET "+route, fmt.Sprintf("status %d, panic in log: %t", status, panicked))
		case status == http.StatusNotFound:
			skipped = append(skipped, route)
		case status >= 400:
			r.fail("GET "+route, "status "+strconv.Itoa(status))
		default:
			r.ok("GET "+route, strconv.Itoa(status))
		}
	}
	groups := map[string]int{}
	var order []string
	for _, route := range skipped {
		pattern := pattern(route)
		if groups[pattern] == 0 {
			order = append(order, pattern)
		}
		groups[pattern]++
	}
	for _, pattern := range order {
		r.skip("GET "+pattern, fmt.Sprintf("404 x%d: not registered in this binary, or no such row", groups[pattern]))
	}
}

func (e *e2e) checkCLI(ctx context.Context, r *report) {
	for _, args := range cliChecks {
		name := "atrium " + strings.Join(args, " ")
		code, stderr := e.cli(ctx, args)
		switch {
		case code == 0:
			r.ok(name, "exit 0")
		case code == 2 && strings.Contains(stderr, "invalid choice"):
			r.skip(name, "not in this binary (invalid choice)")
		default:
			r.fail(name, fmt.Sprintf("exit %d: %s", code, strings.TrimSpace(stderr)))
		}
	}
}

func (e *e2e) readsTable(ctx context.Context) bool {
	mark := e.log.Len()
	for _, route := range e.routes {
		status, body, err := e.get(ctx, route)
		if err != nil || status >= 500 || strings.Contains(body, "no such table") {
			return true
		}
	}
	if strings.Contains(e.log.From(mark), "no such table") {
		return true
	}
	for _, args := range cliChecks {
		if code, stderr := e.cli(ctx, args); code != 0 && strings.Contains(stderr, "no such table") {
			return true
		}
	}
	return false
}
