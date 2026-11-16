package main

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/app"
)

const (
	oldUIPolicy  = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; worker-src 'self'"
	nextUIPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; require-trusted-types-for 'script'; trusted-types atrium-md atrium-sw svelte-trusted-html"
)

type nextReply struct {
	status int
	header http.Header
	body   []byte
}

func nextGet(t *testing.T, base, target string, headers ...string) nextReply {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return nextReply{status: resp.StatusCode, header: resp.Header, body: body}
}

func nextAsset(t *testing.T) (name string, gzipped []byte) {
	t.Helper()
	entries, err := fs.ReadDir(app.FS, "assets")
	if err != nil {
		t.Fatalf("the embedded client has no assets directory; stage it with `just web-stub`: %v", err)
	}
	for _, entry := range entries {
		ext := path.Ext(entry.Name())
		if entry.IsDir() || ext == ".gz" || ext == ".br" {
			continue
		}
		candidate := "assets/" + entry.Name()
		if content, err := fs.ReadFile(app.FS, candidate+".gz"); err == nil {
			return candidate, content
		}
		if name == "" {
			name = candidate
		}
	}
	if name == "" {
		t.Fatal("the embedded client has no hashed asset")
	}
	return name, nil
}

func TestNextClientServesTheShell(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	port := freeLocalPort(t)
	s := startAPISite(t, dir, api, "--emergency-port", port)
	shell, err := fs.ReadFile(app.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	asset, gzipped := nextAsset(t)
	plain, err := fs.ReadFile(app.FS, asset)
	if err != nil {
		t.Fatal(err)
	}

	if signIn := nextGet(t, s.base, "/", "Accept", "text/html"); signIn.status != http.StatusSeeOther || !strings.HasPrefix(signIn.header.Get("Location"), "/login") || signIn.header.Get("Content-Security-Policy") != oldUIPolicy {
		t.Fatalf("signed-out GET / no longer goes to sign-in under the old policy: %d %v", signIn.status, signIn.header)
	}
	if session := nextGet(t, s.base, "/web/v1/session", "Atrium-Client", "web"); session.status != http.StatusUnauthorized {
		t.Fatalf("the public shell opened the web session: %d %s", session.status, session.body)
	}
	if host := nextGet(t, s.base, "/next/", "Accept", "text/html", "Host", "elsewhere.example"); host.status != http.StatusBadRequest {
		t.Fatalf("bad Host on /next/: %d", host.status)
	}

	for _, base := range []string{s.base, "http://127.0.0.1:" + port} {
		for _, target := range []string{"/next/", "/next/apps", "/next/inbox/thread/7?view=full", "/next/index.html"} {
			got := nextGet(t, base, target, "Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
			h := got.header
			if got.status != http.StatusOK || string(got.body) != string(shell) {
				t.Fatalf("%s%s: %d %q", base, target, got.status, got.body)
			}
			if h.Get("Content-Security-Policy") != nextUIPolicy || h.Get("Cache-Control") != "no-cache" || h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("ETag") == "" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("Service-Worker-Allowed") != "" {
				t.Fatalf("%s%s headers: %v", base, target, h)
			}
			if again := nextGet(t, base, target, "Accept", "text/html", "If-None-Match", h.Get("ETag")); again.status != http.StatusNotModified {
				t.Fatalf("%s%s revalidation: %d", base, target, again.status)
			}
		}
		if bare := nextGet(t, base, "/next"); bare.status != http.StatusMovedPermanently || bare.header.Get("Location") != "/next/" {
			t.Fatalf("%s/next: %d %v", base, bare.status, bare.header)
		}
		for _, accept := range []string{"*/*", "text/html"} {
			if missing := nextGet(t, base, "/next/assets/missing-0000.js", "Accept", accept); missing.status != http.StatusNotFound || string(missing.body) == string(shell) {
				t.Fatalf("%s unknown asset with Accept %s: %d %q", base, accept, missing.status, missing.body)
			}
		}
		if script := nextGet(t, base, "/next/sw.js", "Accept", "*/*"); script.status != http.StatusNotFound && script.header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s/next/sw.js: %d %v", base, script.status, script.header)
		}

		identity := nextGet(t, base, "/next/"+asset, "Accept-Encoding", "identity")
		if identity.status != http.StatusOK || string(identity.body) != string(plain) || identity.header.Get("Content-Encoding") != "" {
			t.Fatalf("%s identity %s: %d %v", base, asset, identity.status, identity.header)
		}
		if identity.header.Get("Cache-Control") != "public, max-age=31536000, immutable" || identity.header.Get("Vary") != "Accept-Encoding" || identity.header.Get("Content-Security-Policy") != nextUIPolicy {
			t.Fatalf("%s asset headers: %v", base, identity.header)
		}
		compressed := nextGet(t, base, "/next/"+asset, "Accept-Encoding", "gzip")
		want, encoding := plain, ""
		if gzipped != nil {
			want, encoding = gzipped, "gzip"
		}
		if compressed.status != http.StatusOK || string(compressed.body) != string(want) || compressed.header.Get("Content-Encoding") != encoding || compressed.header.Get("ETag") == identity.header.Get("ETag") && encoding != "" {
			t.Fatalf("%s gzip %s: %d %v", base, asset, compressed.status, compressed.header)
		}
		if refused := nextGet(t, base, "/next/"+asset, "Accept-Encoding", "identity;q=0, *;q=0"); refused.status != http.StatusNotAcceptable {
			t.Fatalf("%s refused encodings: %d", base, refused.status)
		}

		if old := nextGet(t, base, "/static/sw.js"); old.status != http.StatusOK || old.header.Get("Content-Security-Policy") != oldUIPolicy || old.header.Get("Service-Worker-Allowed") != "/" {
			t.Fatalf("%s old worker: %d %v", base, old.status, old.header)
		}
	}
	if about := nextGet(t, "http://127.0.0.1:"+port, "/about", "Accept", "text/html"); about.status != http.StatusOK || about.header.Get("Content-Security-Policy") != oldUIPolicy {
		t.Fatalf("old UI page policy changed: %d %v", about.status, about.header)
	}
}
