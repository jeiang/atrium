package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/media"
)

const vpnHost = "media.vpn.test"

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func vpnTable(addr, peers string) string {
	return "\n[vpn]\nlisten = " + strconv.Quote(addr) + "\nallowed_peers = [" + strconv.Quote(peers) + "]\nallowed_hosts = [" + strconv.Quote(vpnHost) + ", " + strconv.Quote(addr) + "]\nallow_loopback = true\n"
}

func vpnRequest(t *testing.T, method, target, host string, header http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := mediaAgentClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestVPNListenerServesOnlyLibraryFiles(t *testing.T) {
	addr := freeLoopbackAddr(t)
	agent := startMediaAgent(t, mediaAgentOpts{extra: vpnTable(addr, "127.0.0.1/32")})
	episode := "Lanterns/Season 01/Lanterns - S01E01.mkv"
	body := strings.Repeat("0123456789", 10)
	writeMediaFile(t, filepath.Join(agent.roots["tv"], episode), body)
	writeMediaFile(t, filepath.Join(agent.roots["downloads"], "x"), "downloads secret")
	if err := os.Symlink(filepath.Join(agent.roots["downloads"], "x"), filepath.Join(agent.roots["tv"], "Lanterns", "escape.mkv")); err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr
	file := base + "/library/tv/" + (&url.URL{Path: episode}).EscapedPath()
	bearer := http.Header{"Authorization": {"Bearer " + agent.token}}
	waitFor(t, func() bool {
		code, _ := vpnRequest(t, http.MethodHead, file, vpnHost, nil)
		return code == http.StatusOK
	})

	if code, got := vpnRequest(t, http.MethodGet, file, vpnHost, nil); code != http.StatusOK || got != body {
		t.Fatalf("GET = %d %q", code, got)
	}
	if code, got := vpnRequest(t, http.MethodGet, file, "", http.Header{"Range": {"bytes=10-19"}}); code != http.StatusPartialContent || got != body[10:20] {
		t.Fatalf("range GET on the address Host = %d %q", code, got)
	}
	if code, _ := vpnRequest(t, http.MethodPost, file, vpnHost, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", code)
	}
	for _, target := range []string{
		"/v1/files?root=tv&path=" + url.QueryEscape(episode),
		"/health",
		"/v1/library",
		"/library/downloads/x",
		"/library/import/x",
		"/library/tv/",
		"/library/tv",
		media.FilePrefix + "tv/" + (&url.URL{Path: episode}).EscapedPath() + "?exp=4000000000&sig=00",
	} {
		if code, got := vpnRequest(t, http.MethodGet, base+target, vpnHost, bearer); code != http.StatusNotFound || strings.Contains(got, body) || strings.Contains(got, "downloads secret") {
			t.Errorf("GET %s = %d %.200q, want 404", target, code, got)
		}
	}
	for _, target := range []string{"/library/tv/..%2Fdownloads/x", "/library/tv/Lanterns/escape.mkv"} {
		if code, got := vpnRequest(t, http.MethodGet, base+target, vpnHost, nil); code == http.StatusOK || strings.Contains(got, "downloads secret") {
			t.Errorf("GET %s = %d %.200q, escaped the root", target, code, got)
		}
	}
	if code, got := vpnRequest(t, http.MethodGet, file, "evil.test", nil); code != http.StatusBadRequest || strings.Contains(got, body) {
		t.Errorf("foreign Host = %d", code)
	}
	if code, _ := vpnRequest(t, http.MethodGet, file, mediaAgentHost, nil); code != http.StatusBadRequest {
		t.Errorf("control Host on the VPN listener = %d", code)
	}
	if resp, _ := agent.get(t, "/library/tv/"+(&url.URL{Path: episode}).EscapedPath(), nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("VPN path on the control listener = %d", resp.StatusCode)
	}

	agent.terminate(t)
	restarted := startMediaAgent(t, mediaAgentOpts{dir: agent.dir, extra: vpnTable(addr, "10.0.0.0/8")})
	for i := range 300 {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("refused peer connection %d: %v", i, err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte("GET /library/tv/" + (&url.URL{Path: episode}).EscapedPath() + " HTTP/1.1\r\nHost: " + vpnHost + "\r\n\r\n"))
		got, err := io.ReadAll(conn)
		_ = conn.Close()
		if len(got) != 0 || errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("refused peer connection %d was answered or held open: %q %v", i, got, err)
		}
	}
	restarted.terminate(t)

	dir := t.TempDir()
	config, _, _ := writeMediaAgentConfig(t, dir, mediaAgentOpts{extra: "\n[vpn]\nlisten = \"0.0.0.0:" + strings.Split(addr, ":")[1] + "\"\nallowed_peers = [\"10.0.0.0/8\"]\nallowed_hosts = [\"" + vpnHost + "\"]\nallow_loopback = true\n"})
	var stderr strings.Builder
	cmd := mediaAgentCmd(config)
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() == 0 || !strings.Contains(stderr.String(), "vpn.listen must not be a wildcard address") {
		t.Fatalf("wildcard VPN listen: %v %q", err, stderr.String())
	}
}

const (
	oddMovie = "Oddity (2020)"
	oddFile  = ".Ünïcode #1 ?50%.mkv"
)

func TestLibraryLinksUseOneVPNBase(t *testing.T) {
	movieRel := "movies/" + libraryMovie + "/" + libraryMovie + ".mkv"
	search := func(t *testing.T, base string) (*asstWeb, librarySearch, librarySearch, librarySearch, *mediaAgent, string) {
		t.Helper()
		dir, api, _ := moduleFixture(t, flagsExcept("", ""))
		addr := freeLoopbackAddr(t)
		agent := startMediaAgent(t, mediaAgentOpts{main: true, dir: t.TempDir(), extra: vpnTable(addr, "127.0.0.1/32")})
		writeMediaFile(t, filepath.Join(agent.roots["movies"], libraryMovie, libraryMovie+".mkv"), "movie bytes")
		writeMediaFile(t, filepath.Join(agent.roots["tv"], strings.TrimPrefix(libraryEpisode, "tv/")), "episode bytes")
		writeMediaFile(t, filepath.Join(agent.roots["movies"], oddMovie, oddFile), "odd bytes")
		raw, err := os.ReadFile(defaultHostConfig)
		if err != nil {
			t.Fatal(err)
		}
		setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-library.toml", withAgentConnection(string(raw), agent.base, filepath.Join(agent.dir, "token"))))
		value := base
		if base == "vpn" {
			value, base = "HTTP://"+addr+"/", "http://"+addr
		}
		setEnv(t, settingEnv(media.KeyVPNBaseURL), value)
		asstWarm(t)
		d := asstDB(t, dir)
		w := asstBrowse(t, startServeIn(t, dir))
		return w, searchThroughServe(t, api, d, w, "dune"), searchThroughServe(t, api, d, w, "lanterns"), searchThroughServe(t, api, d, w, "oddity"), agent, base
	}

	t.Run("unset", func(t *testing.T) {
		_, movie, show, _, _, _ := search(t, "")
		for _, found := range []librarySearch{movie, show} {
			if len(found.Results) != 1 || len(found.Results[0].VPNURLs) != 1 || found.Results[0].VPNURLs[0] != "" {
				t.Errorf("a VPN link without a base: %+v", found)
			}
		}
	})

	t.Run("set", func(t *testing.T) {
		w, movie, show, odd, _, base := search(t, "vpn")
		cases := []struct {
			found  librarySearch
			rel    string
			prefix string
			body   string
		}{
			{movie, movieRel, base + "/library/movies/", "movie bytes"},
			{show, libraryEpisode, base + "/library/tv/", "episode bytes"},
			{odd, "movies/" + oddMovie + "/" + oddFile, base + "/library/movies/", "odd bytes"},
		}
		for _, c := range cases {
			if len(c.found.Results) != 1 || len(c.found.Results[0].Files) != 1 || c.found.Results[0].Files[0] != c.rel || len(c.found.Results[0].VPNURLs) != 1 {
				t.Fatalf("search %+v", c.found)
			}
			link := c.found.Results[0].VPNURLs[0]
			if !strings.HasPrefix(link, c.prefix) {
				t.Fatalf("VPN link %q, want prefix %q", link, c.prefix)
			}
			if code, got := vpnRequest(t, http.MethodGet, link, "", nil); code != http.StatusOK || got != c.body {
				t.Errorf("GET %s = %d %q", link, code, got)
			}
			path := strings.TrimPrefix(link, base)
			if code, got, _ := w.get(path, false); code == http.StatusOK || strings.Contains(got, c.body) {
				t.Errorf("main served the VPN path %s: %d", path, code)
			}
			if code, got := vpnRequest(t, http.MethodGet, link, "atrium.test", nil); code != http.StatusBadRequest || strings.Contains(got, c.body) {
				t.Errorf("VPN link on the public Host = %d", code)
			}
		}
	})

	t.Run("refused bases", func(t *testing.T) {
		for _, value := range []string{"http://media.example.org", "https://media.example.org/library", "https://user@media.example.org", "https://media.example.org?x=1", "ftp://media.example.org", "https://media.example.org#", "https://media.example.org/#"} {
			setEnv(t, settingEnv(media.KeyVPNBaseURL), value)
			if got := runApp(t, "settings", "list"); got.code == cli.ExitOK || !strings.Contains(strings.ToLower(got.stderr), media.KeyVPNBaseURL) {
				t.Errorf("base %q: exit %d stderr %q", value, got.code, got.stderr)
			}
		}
		for _, value := range []string{"https://media.example.org:8443/", "http://localhost:8483", "http://[::1]:8483"} {
			setEnv(t, settingEnv(media.KeyVPNBaseURL), value)
			if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
				t.Errorf("base %q: exit %d stderr %q", value, got.code, got.stderr)
			}
		}
	})
}

func TestRemovedMediaRootHostKeyIsIgnoredWithAWarning(t *testing.T) {
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	raw, err := os.ReadFile(defaultHostConfig)
	if err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", writeFile(t, dir, "host-media-root.toml", string(raw)+"\n[media]\nmedia_root = \"/var/lib/media\"\n"))
	logs := &lockedBuffer{}
	previous := slog.Default()
	logx.Setup(logs)
	t.Cleanup(func() { slog.SetDefault(previous) })
	if got := runApp(t, "settings", "list"); got.code != cli.ExitOK {
		t.Fatalf("settings list with media_root: exit %d stderr %q", got.code, got.stderr)
	}
	if !strings.Contains(logs.String(), "ignoring a removed host key") || !strings.Contains(logs.String(), "media.media_root") || !strings.Contains(logs.String(), "services.atrium.mediaAgent.roots") {
		t.Errorf("media_root was not warned about: %q", logs.String())
	}
}
