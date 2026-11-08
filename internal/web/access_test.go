package web

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/cli"
)

func runServePages(t *testing.T, ctx context.Context, pages []Page, args ...string) (int, string, string) {
	t.Helper()
	app := cli.New("atrium", nil)
	app.Add(ServeCommand(testBoot(), pages...))
	var stdout, stderr bytes.Buffer
	code := app.Run(ctx, append([]string{"serve"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func startServePages(t *testing.T, pages []Page, args ...string) (string, func() int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_PUBLIC_HOST", "")
	ctx, cancel := context.WithCancel(context.Background())
	port := freePort(t)
	done := make(chan int, 1)
	go func() {
		code, _, _ := runServePages(t, ctx, pages, append([]string{"--port", port}, args...)...)
		done <- code
	}()
	url := "http://127.0.0.1:" + port
	waitForHealthz(t, url)
	return url, func() int {
		cancel()
		return <-done
	}
}

func TestRouteAccessClasses(t *testing.T) {
	page := Page{Home: "/chat", Module: func(r chi.Router, d *Deps) {
		d.Public(http.MethodGet, "/public", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		d.Admin(http.MethodGet, "/admin", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	}}
	url, stop := startServePages(t, []Page{page}, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	defer stop()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, groups string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if groups != "" {
			req.Header.Set("Remote-Groups", groups)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if got := get("/public", "").StatusCode; got != http.StatusNoContent {
		t.Errorf("public route: %d", got)
	}
	if got := get("/admin", "").StatusCode; got != http.StatusForbidden {
		t.Errorf("anonymous admin route: %d", got)
	}
	if got := get("/admin", Group).StatusCode; got != http.StatusForbidden {
		t.Errorf("non-admin admin route: %d", got)
	}
	if got := get("/admin", Group+",atrium_admin").StatusCode; got != http.StatusNoContent {
		t.Errorf("admin route: %d", got)
	}
	root := get("/", Group)
	if root.StatusCode != http.StatusSeeOther || root.Header.Get("Location") != "/chat" {
		t.Errorf("root redirect: %d %q", root.StatusCode, root.Header.Get("Location"))
	}
}

func TestServeStaticGzipVary(t *testing.T) {
	url, stop := startServe(t, "--auth", "local")
	defer stop()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	for _, accept := range []string{"gzip", ""} {
		r, _ := http.NewRequest(http.MethodGet, url+"/static/app.css", nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
			t.Errorf("accept %q: Vary %v", accept, got)
		}
		if accept == "gzip" && resp.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("gzip response encoding %q", resp.Header.Get("Content-Encoding"))
		}
		if accept == "" && resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("plain response encoding %q", resp.Header.Get("Content-Encoding"))
		}
	}
}
