package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
)

func agentConnectionTOML(base, token string) string {
	return "\n[connections.\"service/media-agent\"]\nkind = \"service/media-agent\"\nlabel = \"Media agent\"\nconfig = { url = " + strconv.Quote(base) + " }\ncapabilities = [\"file\", \"stream\"]\ncredential_files = { bearer = " + strconv.Quote(token) + " }\n"
}

func signedAgentServe(t *testing.T, dir, agentBase, token string) string {
	t.Helper()
	keyFile := filepath.Join(dir, "link.key")
	if err := os.WriteFile(keyFile, []byte("proxy-link-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, settingEnv(config.KeyPublicURL), "https://atrium.test")
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-agent.toml", "[app_keys]\nlink_key_file = "+strconv.Quote(keyFile)+"\n"+defaultQbitConnection+agentConnectionTOML(agentBase, token)))
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedAllFlagsIn(t, dir)
	base, _, stop, err := startServeAppArgs(t, newTestApp(t), &lockedBuffer{}, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return base
}

func signedGet(t *testing.T, method, target string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestSignedLinkStreamsFromTheAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	rel := "movies/Dune (2021)/Dune (2021).mkv"
	body := strings.Repeat("0123456789", 20)
	mnpWrite(t, filepath.Join(filepath.Dir(agent.roots["movies"]), rel), []byte(body))
	base := signedAgentServe(t, dir, agent.base, filepath.Join(agent.dir, "token"))
	link := base + mnpSign("proxy-link-key", rel, 4_000_000_000)
	check := func(resp *http.Response, status int) {
		t.Helper()
		if resp.StatusCode != status || resp.Header.Get("Cache-Control") != "private, no-store" || resp.Header.Get("Set-Cookie") != "" {
			t.Fatalf("status/cache/cookie = %d %q %q", resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Set-Cookie"))
		}
	}
	resp, got := signedGet(t, http.MethodGet, link, nil)
	check(resp, 200)
	if got != body {
		t.Fatalf("body length %d", len(got))
	}
	etag := resp.Header.Get("ETag")
	resp, got = signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=2-5"})
	check(resp, 206)
	if got != body[2:6] || resp.Header.Get("Content-Range") != "bytes 2-5/200" {
		t.Fatalf("range = %q %q", got, resp.Header.Get("Content-Range"))
	}
	resp, got = signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=-4"})
	check(resp, 206)
	if got != body[196:] {
		t.Fatalf("suffix = %q", got)
	}
	resp, _ = signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=0-1,4-5"})
	check(resp, 206)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/byteranges") {
		t.Fatalf("multi-range type %q", resp.Header.Get("Content-Type"))
	}
	resp, got = signedGet(t, http.MethodHead, link, nil)
	check(resp, 200)
	if got != "" || resp.Header.Get("Content-Length") != "200" {
		t.Fatalf("HEAD body %q length %q", got, resp.Header.Get("Content-Length"))
	}
	resp, got = signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=2-5", "If-Range": `"other"`})
	check(resp, 200)
	if got != body {
		t.Fatal("If-Range mismatch did not return the full body")
	}
	resp, _ = signedGet(t, http.MethodGet, link, map[string]string{"If-None-Match": etag})
	check(resp, 304)
	resp, _ = signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=999-"})
	check(resp, 416)
}

func TestInvalidSignedLinksNeverReachTheAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var count atomic.Int64
	token := filepath.Join(dir, "agent.token")
	if err := os.WriteFile(token, []byte(strings.Repeat("s", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 40) {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(agent.Close)
	base := signedAgentServe(t, dir, agent.URL, token)
	rel := "movies/Dune/Dune.mkv"
	good := mnpSign("proxy-link-key", rel, 4_000_000_000)
	for _, tc := range []struct {
		target string
		host   string
		status int
	}{
		{mnpFlipLast(good), "", 403},
		{mnpSign("proxy-link-key", rel, 1), "", 403},
		{good, "evil.test", 400},
	} {
		resp, _ := signedGet(t, http.MethodGet, base+tc.target, map[string]string{"Host": tc.host})
		if resp.StatusCode != tc.status {
			t.Fatalf("%s = %d, want %d", tc.target, resp.StatusCode, tc.status)
		}
	}
	if count.Load() != 0 {
		t.Fatalf("invalid links reached the agent %d times", count.Load())
	}
	resp, got := signedGet(t, http.MethodGet, base+good, nil)
	if resp.StatusCode != 200 || got != "ok" || count.Load() != 1 {
		t.Fatalf("valid link = %d %q count %d", resp.StatusCode, got, count.Load())
	}
}

func TestSignedStreamCancelReachesTheAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	rel := "movies/Large/Large.mkv"
	mnpWrite(t, filepath.Join(filepath.Dir(agent.roots["movies"]), rel), make([]byte, 64<<20))
	base := signedAgentServe(t, dir, agent.base, filepath.Join(agent.dir, "token"))
	link := base + mnpSign("proxy-link-key", rel, 4_000_000_000)
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	ranged, body := signedGet(t, http.MethodGet, link, map[string]string{"Range": "bytes=0-3"})
	if ranged.StatusCode != 206 || len(body) != 4 {
		t.Fatalf("follow-up range = %d %d", ranged.StatusCode, len(body))
	}
	if took := agent.terminate(t); took > 15*time.Second {
		t.Fatalf("agent shutdown took %s", took)
	}
}

func TestMediaAgentConnectionTest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: dir})
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "agent.toml", agentConnectionTOML(agent.base, filepath.Join(agent.dir, "token"))))
	ran := runApp(t, "connections", "test", "service/media-agent", "--json")
	if ran.code != cli.ExitOK || !strings.Contains(ran.stdout, `"state": "ok"`) || strings.Contains(ran.stdout, agent.token) {
		t.Fatalf("connection test = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	wrong := filepath.Join(dir, "wrong.token")
	if err := os.WriteFile(wrong, []byte(strings.Repeat("x", 48)), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "wrong.toml", agentConnectionTOML(agent.base, wrong)))
	ran = runApp(t, "connections", "test", "service/media-agent", "--json")
	if !strings.Contains(ran.stdout, `"state": "needs_reauth"`) || strings.Contains(ran.stdout+ran.stderr, strings.Repeat("x", 48)) {
		t.Fatalf("wrong token = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"protocol":2,"agent_id":"a","root_revision":"r","capabilities":["file","stream"],"ready":true,"reason_code":""}`))
	}))
	fake.Listener = listener
	fake.Start()
	t.Cleanup(fake.Close)
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "fake.toml", agentConnectionTOML(fake.URL, filepath.Join(agent.dir, "token"))))
	ran = runApp(t, "connections", "test", "service/media-agent", "--json")
	if !strings.Contains(ran.stdout, `"state": "failing"`) {
		t.Fatalf("protocol mismatch = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
}
