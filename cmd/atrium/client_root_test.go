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
	serverPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; worker-src 'self'"
	clientPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; require-trusted-types-for 'script'; trusted-types atrium-md atrium-sw svelte-trusted-html"
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

func TestClientServesAtRoot(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	port, flags := handOverEmergency(t)
	s := startAPISite(t, dir, api, flags...)
	shell, err := fs.ReadFile(app.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := fs.ReadFile(app.FS, "sw.js")
	if err != nil {
		t.Fatal(err)
	}
	asset, gzipped := nextAsset(t)
	plain, err := fs.ReadFile(app.FS, asset)
	if err != nil {
		t.Fatal(err)
	}

	if session := nextGet(t, s.base, "/web/v1/session", "Atrium-Client", "web"); session.status != http.StatusUnauthorized {
		t.Fatalf("the public shell opened the web session: %d %s", session.status, session.body)
	}
	if host := nextGet(t, s.base, "/", "Accept", "text/html", "Host", "elsewhere.example"); host.status != http.StatusBadRequest {
		t.Fatalf("bad Host on /: %d", host.status)
	}
	if form := nextGet(t, s.base, "/inbox", "Accept", "text/html", "Atrium-Client", "web"); form.status != http.StatusOK || string(form.body) != string(shell) {
		t.Fatalf("deep link with the client header: %d", form.status)
	}

	for _, base := range []string{s.base, "http://127.0.0.1:" + port} {
		for _, target := range []string{"/", "/apps", "/inbox/item/7?view=full", "/settings/general", "/index.html", "/chat/12"} {
			got := nextGet(t, base, target, "Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
			h := got.header
			if got.status != http.StatusOK || string(got.body) != string(shell) {
				t.Fatalf("%s%s: %d %q", base, target, got.status, got.body)
			}
			if h.Get("Content-Security-Policy") != clientPolicy || h.Get("Cache-Control") != "no-cache" || h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("ETag") == "" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("Service-Worker-Allowed") != "" {
				t.Fatalf("%s%s headers: %v", base, target, h)
			}
			if again := nextGet(t, base, target, "Accept", "text/html", "If-None-Match", h.Get("ETag")); again.status != http.StatusNotModified {
				t.Fatalf("%s%s revalidation: %d", base, target, again.status)
			}
		}
		for target, want := range map[string]string{
			"/next":                                "/",
			"/next/":                               "/",
			"/next/apps/mail?tab=rules":            "/apps/mail?tab=rules",
			"/next//elsewhere.example/x":           "/elsewhere.example/x",
			"/inbox/":                              "/inbox",
			"//elsewhere.example/":                 "/elsewhere.example",
			"/review":                              "/inbox",
			"/rules/4/edit":                        "/apps/mail/rules/4",
			"/messages/9/correct?x=1":              "/apps/mail/message/9?x=1",
			"/library/knowledge?state=unconfirmed": "/knowledge/review",
			"/settings/usage":                      "/assistant/spend",
			"/about":                               "/settings/general",
			"/apps/media/torrents/ab12/files":      "/apps/torrents",
		} {
			got := nextGet(t, base, target, "Accept", "text/html")
			if got.status != http.StatusMovedPermanently || got.header.Get("Location") != want {
				t.Fatalf("%s%s: %d %q, want 301 %q", base, target, got.status, got.header.Get("Location"), want)
			}
		}
		if callback := nextGet(t, base, "/settings/connections/google/callback?state=s&code=c"); callback.status != http.StatusSeeOther || callback.header.Get("Location") != "/settings/connections/google/default?state=s&code=c" {
			t.Fatalf("%s google callback: %d %v", base, callback.status, callback.header)
		}
		for _, accept := range []string{"*/*", "text/html"} {
			if missing := nextGet(t, base, "/assets/missing-0000.js", "Accept", accept); missing.status != http.StatusNotFound || string(missing.body) == string(shell) {
				t.Fatalf("%s unknown asset with Accept %s: %d %q", base, accept, missing.status, missing.body)
			}
		}
		if data := nextGet(t, base, "/favicon-missing.png", "Accept", "image/png"); data.status != http.StatusNotFound {
			t.Fatalf("%s non-HTML miss: %d", base, data.status)
		}
		for _, target := range []string{"/sw.js", "/next/sw.js"} {
			script := nextGet(t, base, target, "Accept", "*/*", "Accept-Encoding", "identity")
			if script.status != http.StatusOK || string(script.body) != string(worker) || script.header.Get("Cache-Control") != "no-cache" || script.header.Get("Content-Security-Policy") != clientPolicy {
				t.Fatalf("%s%s: %d %v", base, target, script.status, script.header)
			}
		}

		identity := nextGet(t, base, "/"+asset, "Accept-Encoding", "identity")
		if identity.status != http.StatusOK || string(identity.body) != string(plain) || identity.header.Get("Content-Encoding") != "" {
			t.Fatalf("%s identity %s: %d %v", base, asset, identity.status, identity.header)
		}
		if identity.header.Get("Cache-Control") != "public, max-age=31536000, immutable" || identity.header.Get("Vary") != "Accept-Encoding" || identity.header.Get("Content-Security-Policy") != clientPolicy {
			t.Fatalf("%s asset headers: %v", base, identity.header)
		}
		compressed := nextGet(t, base, "/"+asset, "Accept-Encoding", "gzip")
		want, encoding := plain, ""
		if gzipped != nil {
			want, encoding = gzipped, "gzip"
		}
		if compressed.status != http.StatusOK || string(compressed.body) != string(want) || compressed.header.Get("Content-Encoding") != encoding || compressed.header.Get("ETag") == identity.header.Get("ETag") && encoding != "" {
			t.Fatalf("%s gzip %s: %d %v", base, asset, compressed.status, compressed.header)
		}
		if refused := nextGet(t, base, "/"+asset, "Accept-Encoding", "identity;q=0, *;q=0"); refused.status != http.StatusNotAcceptable {
			t.Fatalf("%s refused encodings: %d", base, refused.status)
		}
		for _, gone := range []string{"/static/sw.js", "/static/app.js"} {
			if old := nextGet(t, base, gone, "Accept", "*/*"); old.status != http.StatusNotFound {
				t.Fatalf("%s%s still served: %d", base, gone, old.status)
			}
		}
		if post := nextPost(t, base, "/inbox"); post.status == http.StatusOK {
			t.Fatalf("%s POST to a client path reached the shell", base)
		}
	}
	if healthz := nextGet(t, s.base, "/healthz"); healthz.status != http.StatusOK || healthz.header.Get("Content-Security-Policy") != serverPolicy {
		t.Fatalf("server policy changed: %d %v", healthz.status, healthz.header)
	}
}

func nextPost(t *testing.T, base, target string) nextReply {
	t.Helper()
	resp, err := http.Post(base+target, "application/x-www-form-urlencoded", strings.NewReader(""))
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
