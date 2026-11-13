package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const mediaAgentHost = "media-agent.test"

type mediaAgentOpts struct {
	group   string
	tv      string
	control string
	extra   string
	env     []string
	dir     string
}

type mediaAgent struct {
	dir    string
	base   string
	addr   string
	token  string
	roots  map[string]string
	config string
	cmd    *exec.Cmd
	logs   *lockedBuffer
	done   chan struct{}
	err    error
}

func writeMediaAgentConfig(t *testing.T, dir string, opts mediaAgentOpts) (string, map[string]string, string) {
	t.Helper()
	roots := map[string]string{}
	for _, name := range []string{"movies", "tv", "downloads", "import"} {
		roots[name] = filepath.Join(dir, "library", name)
		if err := os.MkdirAll(roots[name], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if opts.tv != "" {
		roots["tv"] = opts.tv
	}
	if opts.group != "" {
		for _, root := range roots {
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if err := os.Lchown(p, -1, os.Getgid()); err != nil {
					return err
				}
				if d.IsDir() {
					return os.Chmod(p, 0o775)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	tokenPath := filepath.Join(dir, "token")
	token, err := os.ReadFile(tokenPath)
	if errors.Is(err, fs.ErrNotExist) {
		raw := make([]byte, 24)
		_, _ = rand.Read(raw)
		token = []byte(hex.EncodeToString(raw))
		if err := os.WriteFile(tokenPath, append(token, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	cfg := `state_directory = "` + filepath.Join(dir, "state") + `"
cache_directory = "` + filepath.Join(dir, "cache") + `"

[control]
listen = "127.0.0.1:0"
token_file = "` + tokenPath + `"
allowed_hosts = ["` + mediaAgentHost + `"]
` + opts.control + `

[roots]
movies = "` + roots["movies"] + `"
tv = "` + roots["tv"] + `"
downloads = "` + roots["downloads"] + `"
import = "` + roots["import"] + `"
` + opts.extra
	if opts.group != "" {
		cfg = "group = " + strconv.Quote(opts.group) + "\n" + cfg
	}
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, roots, strings.TrimSpace(string(token))
}

func mediaAgentCmd(config string, env ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "media-agent", "--config", config)
	cmd.Env = append(append(os.Environ(), execMainEnv+"=1"), env...)
	return cmd
}

func startMediaAgent(t *testing.T, opts mediaAgentOpts) *mediaAgent {
	t.Helper()
	dir := opts.dir
	if dir == "" {
		dir = t.TempDir()
	}
	config, roots, token := writeMediaAgentConfig(t, dir, opts)
	a := &mediaAgent{dir: dir, token: token, roots: roots, config: config, logs: &lockedBuffer{}, done: make(chan struct{})}
	a.cmd = mediaAgentCmd(config, opts.env...)
	a.cmd.Stderr = a.logs
	out, err := a.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1)
	go func() {
		r := bufio.NewReader(out)
		line, _ := r.ReadString('\n')
		lines <- line
		_, _ = a.logs.Write([]byte(line))
		_, _ = io.Copy(a.logs, r)
	}()
	go func() {
		a.err = a.cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		_ = a.cmd.Process.Kill()
		<-a.done
	})
	select {
	case line := <-lines:
		addr, ok := strings.CutPrefix(strings.TrimSpace(line), "media agent listening on ")
		if !ok {
			<-a.done
			t.Fatalf("media agent did not start: %q %s", line, a.logs.String())
		}
		a.addr, a.base = addr, "http://"+addr
	case <-time.After(30 * time.Second):
		t.Fatalf("media agent did not report its address: %s", a.logs.String())
	}
	if strings.Contains(opts.control, `allowed_peers = ["192.0.2.0/24"]`) {
		return a
	}
	waitFor(t, func() bool {
		resp, _ := a.get(t, "/health", nil)
		return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusForbidden
	})
	return a
}

var mediaAgentClient = &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}

func (a *mediaAgent) do(t *testing.T, method, target string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, a.base+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = mediaAgentHost
	req.Header.Set("Authorization", "Bearer "+a.token)
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
	return resp, body
}

func (a *mediaAgent) get(t *testing.T, target string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	return a.do(t, http.MethodGet, target, header)
}

func (a *mediaAgent) terminate(t *testing.T) time.Duration {
	t.Helper()
	start := time.Now()
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("media agent did not stop within 15s: %s", a.logs.String())
	}
	if a.err != nil {
		t.Fatalf("media agent exit: %v %s", a.err, a.logs.String())
	}
	return time.Since(start)
}

func problemCode(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Code     string `json:"code"`
		Mutation string `json:"mutation"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("problem body %q: %v", body, err)
	}
	if p.Mutation != "none" {
		t.Fatalf("problem mutation = %q", p.Mutation)
	}
	return p.Code
}

func writeMediaFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMediaAgentStartsWithoutAnyDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missingDB := filepath.Join(dir, "nothing", "atrium.db")
	a := startMediaAgent(t, mediaAgentOpts{dir: dir, env: []string{"ATRIUM_DB=" + missingDB, "ATRIUM_CACHE_DIR=" + filepath.Join(dir, "maincache")}})
	resp, body := a.get(t, "/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d %s", resp.StatusCode, body)
	}
	var h struct {
		Protocol     int      `json:"protocol"`
		AgentID      string   `json:"agent_id"`
		RootRevision string   `json:"root_revision"`
		Capabilities []string `json:"capabilities"`
		Ready        bool     `json:"ready"`
		ReasonCode   string   `json:"reason_code"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatal(err)
	}
	if h.Protocol != 1 || !h.Ready || h.ReasonCode != "" || !slices.Equal(slices.DeleteFunc(h.Capabilities, func(c string) bool { return c == "thumbnail" }), []string{"file", "scan", "stream"}) {
		t.Fatalf("health = %+v", h)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(h.AgentID) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(h.RootRevision) {
		t.Fatalf("health identities = %+v", h)
	}
	if strings.Contains(string(body), dir) || strings.Contains(string(body), a.token) {
		t.Fatalf("health leaks a path or the token: %s", body)
	}
	a.terminate(t)
	if _, err := os.Stat(missingDB); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("database path touched: %v", err)
	}
	var outside []string
	for _, p := range treeOf(t, dir) {
		if p != "state" && p != "cache" && !strings.HasPrefix(p, "state/") && !strings.HasPrefix(p, "cache/") {
			outside = append(outside, p)
		}
	}
	want := []string{".", "agent.toml", "library", "library/downloads", "library/import", "library/movies", "library/tv", "token"}
	if !slices.Equal(outside, want) {
		t.Fatalf("files outside state and cache = %v", outside)
	}
	for _, d := range []string{"state", "cache"} {
		info, err := os.Stat(filepath.Join(dir, d))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s directory = %v %v", d, info, err)
		}
	}
}

func TestMediaAgentRefusesUnsafeConfig(t *testing.T) {
	t.Parallel()
	refuse := func(t *testing.T, opts mediaAgentOpts, prepare func(dir string), _ string) {
		t.Helper()
		dir := t.TempDir()
		config, _, token := writeMediaAgentConfig(t, dir, opts)
		if prepare != nil {
			prepare(dir)
		}
		var stderr, stdout strings.Builder
		cmd := mediaAgentCmd(config)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("exit = %v, stderr %q", err, stderr.String())
		}
		if stdout.Len() != 0 || strings.Contains(stderr.String(), token) {
			t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
		}
	}
	for _, addr := range []string{"[::ffff:0.0.0.0]:0", "[::%1]:0", "[::ffff:127.0.0.1]:0"} {
		for _, section := range []string{"control", "vpn"} {
			t.Run(section+addr, func(t *testing.T) {
				refuse(t, mediaAgentOpts{}, func(dir string) {
					cfg := filepath.Join(dir, "agent.toml")
					if section == "control" {
						rewrite(t, cfg, `listen = "127.0.0.1:0"`, `listen = "`+addr+`"`)
					} else {
						f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
						if err != nil {
							t.Fatal(err)
						}
						_, err = f.WriteString("\n[vpn]\nlisten = " + strconv.Quote(addr) + "\n")
						_ = f.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
				}, section+".listen")
			})
		}
	}
	for _, field := range []string{"token_file", "tls_cert", "tls_key"} {
		t.Run("relative "+field, func(t *testing.T) {
			refuse(t, mediaAgentOpts{}, func(dir string) {
				cfg := filepath.Join(dir, "agent.toml")
				if field == "token_file" {
					rewrite(t, cfg, `token_file = "`+filepath.Join(dir, "token")+`"`, `token_file = "token"`)
				} else {
					rewrite(t, cfg, `allowed_hosts =`, `tls_cert = "cert"
tls_key = "key"
allowed_hosts =`)
				}
			}, "")
		})
	}
	t.Run("short token", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "token"), []byte("a"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "")
	})
	t.Run("resolved overlap", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			movies := filepath.Join(dir, "library", "movies")
			if err := os.Mkdir(filepath.Join(movies, "nested"), 0o755); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(dir, "alias")
			if err := os.Symlink(movies, alias); err != nil {
				t.Fatal(err)
			}
			rewrite(t, filepath.Join(dir, "agent.toml"), `tv = "`+filepath.Join(dir, "library", "tv")+`"`, `tv = "`+filepath.Join(alias, "nested")+`"`)
		}, "")
	})
	vpn := "\n[vpn]\nlisten = \"127.0.0.1:8483\"\nallowed_peers = [\"127.0.0.0/8\"]\nallowed_hosts = [\"media.vpn.test\"]\n"
	t.Run("loopback vpn without allow_loopback", func(t *testing.T) {
		refuse(t, mediaAgentOpts{extra: vpn}, nil, "set vpn.allow_loopback")
	})
	t.Run("vpn is not served yet", func(t *testing.T) {
		refuse(t, mediaAgentOpts{extra: vpn + "allow_loopback = true\n"}, nil, "vpn: unsupported")
	})
	t.Run("wildcard vpn", func(t *testing.T) {
		refuse(t, mediaAgentOpts{extra: "\n[vpn]\nlisten = \"0.0.0.0:8483\"\nallow_loopback = true\n"}, nil, "vpn.listen must not be a wildcard address")
	})
	t.Run("wildcard control", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			rewrite(t, filepath.Join(dir, "agent.toml"), `listen = "127.0.0.1:0"`, `listen = "[::]:0"`)
		}, "control.listen must not be a wildcard address")
	})
	t.Run("non-loopback control without tls", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			rewrite(t, filepath.Join(dir, "agent.toml"), `listen = "127.0.0.1:0"`, `listen = "192.0.2.10:8482"`)
		}, "control.tls_cert and control.tls_key are required")
	})
	t.Run("unknown key", func(t *testing.T) {
		refuse(t, mediaAgentOpts{control: "allowed_origins = [\"x\"]"}, nil, "unknown keys: control.allowed_origins")
	})
	t.Run("overlapping roots", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			rewrite(t, filepath.Join(dir, "agent.toml"), `tv = "`+filepath.Join(dir, "library", "tv")+`"`, `tv = "`+filepath.Join(dir, "library", "movies", "tv")+`"`)
		}, "roots.movies and roots.tv overlap")
	})
	t.Run("state inside a root", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			rewrite(t, filepath.Join(dir, "agent.toml"), `state_directory = "`+filepath.Join(dir, "state")+`"`, `state_directory = "`+filepath.Join(dir, "library", "import", "state")+`"`)
		}, "state_directory and roots.import overlap")
	})
	t.Run("relative root", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			rewrite(t, filepath.Join(dir, "agent.toml"), `movies = "`+filepath.Join(dir, "library", "movies")+`"`, `movies = "library/movies"`)
		}, "roots.movies must be a clean absolute path")
	})
	t.Run("symlinked root", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			movies := filepath.Join(dir, "library", "movies")
			if err := os.Rename(movies, filepath.Join(dir, "real-movies")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "real-movies"), movies); err != nil {
				t.Fatal(err)
			}
		}, "roots.movies must not be a symbolic link")
	})
	t.Run("empty token", func(t *testing.T) {
		refuse(t, mediaAgentOpts{}, func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "token"), []byte("\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "control.token_file is empty")
	})
	t.Run("usage", func(t *testing.T) {
		var stdout, stderr strings.Builder
		cmd := exec.Command(os.Args[0], "media-agent")
		cmd.Env = append(os.Environ(), execMainEnv+"=1")
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		var exit *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("no config: %v %q", err, stderr.String())
		}
		stdout.Reset()
		cmd = exec.Command(os.Args[0], "media-agent", "--help")
		cmd.Env = append(os.Environ(), execMainEnv+"=1")
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil || !strings.Contains(stdout.String(), "usage: atrium media-agent") {
			t.Fatalf("help: %v %q", err, stdout.String())
		}
		stdout.Reset()
		cmd = exec.Command(os.Args[0], "--help")
		cmd.Env = append(os.Environ(), execMainEnv+"=1")
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil || strings.Contains(stdout.String(), "media-agent") || !strings.Contains(stdout.String(), "usage: atrium") {
			t.Fatalf("main help: %v %q", err, stdout.String())
		}
	})
}

func rewrite(t *testing.T, path, old, replacement string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s lacks %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAgentRefusesWrongBearerAndHost(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["movies"], "Film.mkv"), "film")
	unauthorized := func(name, target string, header http.Header, host string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, a.base+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header = header
		resp, err := mediaAgentClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || problemCode(t, body) != "unauthorized" || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	for _, target := range []string{"/health", "/v1/files?root=movies&path=Film.mkv", "/nowhere"} {
		unauthorized("no bearer "+target, target, http.Header{}, mediaAgentHost)
		unauthorized("wrong bearer "+target, target, http.Header{"Authorization": {"Bearer " + a.token + "x"}}, mediaAgentHost)
		unauthorized("prefix bearer "+target, target, http.Header{"Authorization": {"Bearer " + a.token[:len(a.token)-1]}}, mediaAgentHost)
		unauthorized("basic "+target, target, http.Header{"Authorization": {"Basic " + a.token}}, mediaAgentHost)
		unauthorized("two headers "+target, target, http.Header{"Authorization": {"Bearer " + a.token, "Bearer " + a.token}}, mediaAgentHost)
		unauthorized("cookie "+target, target, http.Header{"Cookie": {"token=" + a.token}}, mediaAgentHost)
	}
	unauthorized("query token", "/health?token="+a.token, http.Header{}, mediaAgentHost)
	unauthorized("query access_token", "/v1/files?root=movies&path=Film.mkv&access_token="+a.token, http.Header{}, mediaAgentHost)
	for _, host := range []string{a.addr, "localhost", "media-agent.test.evil", "MEDIA-AGENT.TEST:80"} {
		req, _ := http.NewRequest(http.MethodGet, a.base+"/health", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := mediaAgentClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || problemCode(t, body) != "invalid_request" {
			t.Fatalf("host %q: %d %s", host, resp.StatusCode, body)
		}
	}
	if resp, body := a.get(t, "/v1/files?root=movies&path=Film.mkv", nil); resp.StatusCode != http.StatusOK || string(body) != "film" {
		t.Fatalf("authorized: %d %q", resp.StatusCode, body)
	}
	a.terminate(t)

	far := startMediaAgent(t, mediaAgentOpts{control: `allowed_peers = ["192.0.2.0/24"]`})
	req, _ := http.NewRequest(http.MethodGet, far.base+"/health", nil)
	req.Host = mediaAgentHost
	req.Header.Set("Authorization", "Bearer "+far.token)
	if resp, err := mediaAgentClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("disallowed peer was accepted")
	}
	near := startMediaAgent(t, mediaAgentOpts{control: `allowed_peers = ["192.0.2.0/24", "127.0.0.0/8"]`})
	if resp, body := near.get(t, "/health", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("peer inside allowed_peers: %d %s", resp.StatusCode, body)
	}
}

func TestMediaAgentStreamsRootedFilesWithRanges(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	movies := a.roots["movies"]
	outside := filepath.Join(a.dir, "outside")
	const content = "0123456789abcdef"
	const secret = "SECRET-OUTSIDE-THE-LIBRARY"
	writeMediaFile(t, filepath.Join(movies, "Film (2020)", "Film.mkv"), content)
	writeMediaFile(t, filepath.Join(movies, "%2e%2e"), "literal")
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "Film.mkv"), secret)
	writeMediaFile(t, filepath.Join(a.roots["import"], "Film.mkv"), secret)
	writeMediaFile(t, filepath.Join(a.dir, "library", "secret.mkv"), secret)
	writeMediaFile(t, filepath.Join(outside, "secret.mkv"), secret)
	writeMediaFile(t, filepath.Join(outside, "show", "ep.mkv"), secret)
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(movies, "leak.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(movies, "leakdir")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(movies, "pipe.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("/tmp", "agent-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(short) }()
	sock, err := net.Listen("unix", filepath.Join(short, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sock.Close() }()
	if err := os.Rename(filepath.Join(short, "socket"), filepath.Join(movies, "socket")); err != nil {
		t.Fatal(err)
	}
	const film = "/v1/files?root=movies&path=Film%20%282020%29%2FFilm.mkv"

	resp, body := a.get(t, film, nil)
	etag, modified := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if resp.StatusCode != http.StatusOK || string(body) != content || resp.Header.Get("Content-Type") != "video/x-matroska" ||
		resp.Header.Get("Accept-Ranges") != "bytes" || !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) || modified == "" {
		t.Fatalf("full: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = a.do(t, http.MethodHead, film, nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 || resp.Header.Get("Content-Length") != "16" || resp.Header.Get("ETag") != etag {
		t.Fatalf("head: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	ranged := func(header http.Header, status int, want, contentRange string) {
		t.Helper()
		resp, body := a.get(t, film, header)
		if resp.StatusCode != status || string(body) != want || resp.Header.Get("Content-Range") != contentRange {
			t.Fatalf("%v: %d %q %q", header, resp.StatusCode, body, resp.Header.Get("Content-Range"))
		}
	}
	ranged(http.Header{"Range": {"bytes=2-5"}}, http.StatusPartialContent, "2345", "bytes 2-5/16")
	ranged(http.Header{"Range": {"bytes=-3"}}, http.StatusPartialContent, "def", "bytes 13-15/16")
	ranged(http.Header{"Range": {"bytes=14-"}}, http.StatusPartialContent, "ef", "bytes 14-15/16")
	ranged(http.Header{"Range": {"bytes=2-5"}, "If-Range": {etag}}, http.StatusPartialContent, "2345", "bytes 2-5/16")
	ranged(http.Header{"Range": {"bytes=2-5"}, "If-Range": {`"0-0-0-0"`}}, http.StatusOK, content, "")
	ranged(http.Header{"If-None-Match": {etag}}, http.StatusNotModified, "", "")
	ranged(http.Header{"If-Modified-Since": {modified}}, http.StatusNotModified, "", "")
	ranged(http.Header{"If-None-Match": {`"0-0-0-0"`}}, http.StatusOK, content, "")
	resp, body = a.get(t, film, http.Header{"Range": {"bytes=100-200"}})
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") != "bytes */16" || problemCode(t, body) != "invalid_request" {
		t.Fatalf("unsatisfiable: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = a.get(t, film, http.Header{"Range": {"bytes=0-1,4-5"}})
	kind, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusPartialContent || err != nil || kind != "multipart/byteranges" {
		t.Fatalf("multi-range: %d %v %v", resp.StatusCode, resp.Header, err)
	}
	mr := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Range")+"="+string(b))
	}
	if !slices.Equal(parts, []string{"bytes 0-1/16=01", "bytes 4-5/16=45"}) {
		t.Fatalf("multi-range parts = %q", parts)
	}

	if resp, body := a.get(t, "/v1/files?root=movies&path=%252e%252e", nil); resp.StatusCode != http.StatusOK || string(body) != "literal" {
		t.Fatalf("query decoded more than once: %d %q", resp.StatusCode, body)
	}
	if resp, body := a.do(t, http.MethodPost, film, nil); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("post: %d %s", resp.StatusCode, body)
	}
	expect := func(query string, status int, code string) {
		t.Helper()
		resp, body := a.get(t, "/v1/files?"+query, nil)
		if strings.Contains(string(body), secret) {
			t.Fatalf("%s leaked bytes from outside the root", query)
		}
		if resp.StatusCode != status || problemCode(t, body) != code {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, body)
		}
	}
	for _, q := range []string{"root=downloads&path=Film.mkv", "root=import&path=Film.mkv"} {
		expect(q, http.StatusBadRequest, "unsafe_path")
	}
	for _, q := range []string{
		"root=library&path=secret.mkv", "root=&path=Film.mkv", "path=Film.mkv", "root=movies",
		"root=movies&path=Film.mkv&path=leak.mkv", "root=movies&root=tv&path=Film.mkv", "root=movies&path=Film.mkv&token=x",
		"root=movies&path=%zz",
	} {
		expect(q, http.StatusBadRequest, "invalid_request")
	}
	expect("root=movies&path="+strings.Repeat("x", 300), http.StatusBadRequest, "unsafe_path")
	ranged(http.Header{"Range": {"bytes=" + strings.Repeat("0-0,", 16) + "0-0"}}, http.StatusOK, content, "")
	abs := filepath.Join(outside, "secret.mkv")
	for _, rel := range []string{
		"..%2Fsecret.mkv", "%2e%2e%2Fsecret.mkv", "%2E%2E/secret.mkv", "../library/secret.mkv", "Film%20(2020)/../../secret.mkv",
		"%2F" + strings.TrimPrefix(abs, "/"), abs, "%2Fetc%2Fpasswd", "..%5Csecret.mkv", "Film%20(2020)%5C..%5C..%5Csecret.mkv",
		"Film.mkv%00.txt", "%00", "%ff%fe", ".%2FFilm.mkv", "Film%20(2020)%2F%2FFilm.mkv", "Film%20(2020)%2F", "",
		"leak.mkv", "leakdir%2Fsecret.mkv", "leakdir%2Fshow%2Fep.mkv",
	} {
		expect("root=movies&path="+rel, http.StatusBadRequest, "unsafe_path")
	}
	for _, rel := range []string{"Film%20(2020)", "missing.mkv", "Film%20(2020)%2FFilm.mkv%2Fx", "pipe.mkv", "socket", "%252e%252e%2Fsecret.mkv"} {
		expect("root=movies&path="+rel, http.StatusNotFound, "not_found")
	}
	if os.Geteuid() != 0 {
		writeMediaFile(t, filepath.Join(movies, "locked.mkv"), secret)
		if err := os.Chmod(filepath.Join(movies, "locked.mkv"), 0); err != nil {
			t.Fatal(err)
		}
		expect("root=movies&path=locked.mkv", http.StatusUnprocessableEntity, "permissions")
	}
	if resp, body := a.get(t, "/v1/files/", nil); resp.StatusCode != http.StatusNotFound || problemCode(t, body) != "not_found" {
		t.Fatalf("unknown endpoint: %d %s", resp.StatusCode, body)
	}

	show := filepath.Join(movies, "show")
	writeMediaFile(t, filepath.Join(show, "ep.mkv"), "inside")
	var swapErr error
	var refused atomic.Int32
	var stop atomic.Bool
	var swaps sync.WaitGroup
	swaps.Go(func() {
		for !stop.Load() {
			if err := os.Rename(show, show+".real"); err != nil {
				swapErr = err
				return
			}
			if err := os.Symlink(filepath.Join(outside, "show"), show); err != nil {
				swapErr = err
				return
			}
			time.Sleep(time.Millisecond)
			if err := os.Remove(show); err != nil {
				swapErr = err
				return
			}
			if err := os.Rename(show+".real", show); err != nil {
				swapErr = err
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	var leaks, served atomic.Int32
	var reqs sync.WaitGroup
	for range 8 {
		reqs.Go(func() {
			for range 60 {
				req, _ := http.NewRequest(http.MethodGet, a.base+"/v1/files?root=movies&path=show%2Fep.mkv", nil)
				req.Host = mediaAgentHost
				req.Header.Set("Authorization", "Bearer "+a.token)
				resp, err := mediaAgentClient.Do(req)
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if strings.Contains(string(body), secret) {
					leaks.Add(1)
				}
				if resp.StatusCode == http.StatusBadRequest {
					var p struct {
						Code string `json:"code"`
					}
					if json.Unmarshal(body, &p) == nil && p.Code == "unsafe_path" {
						refused.Add(1)
					}
				}
				if resp.StatusCode == http.StatusOK && string(body) == "inside" {
					served.Add(1)
				}
			}
		})
	}
	reqs.Wait()
	stop.Store(true)
	swaps.Wait()
	if swapErr != nil {
		t.Fatal(swapErr)
	}
	if refused.Load() == 0 || served.Load() == 0 {
		t.Fatalf("race not observed: refused=%d served=%d", refused.Load(), served.Load())
	}
	if leaks.Load() != 0 {
		t.Fatalf("a symlink swapped in during the walk leaked %d responses", leaks.Load())
	}
	if resp, body := a.get(t, "/v1/files?root=movies&path=show%2Fep.mkv", nil); resp.StatusCode != http.StatusOK || string(body) != "inside" {
		t.Fatalf("after swaps: %d %q (served %d during swaps)", resp.StatusCode, body, served.Load())
	}
}

func TestMediaAgentSecondInstanceAndShutdown(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	_, body := a.get(t, "/health", nil)
	var first struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	second := mediaAgentCmd(a.config)
	second.Stdout, second.Stderr = &stdout, &stderr
	var exit *exec.ExitError
	if err := second.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 {
		t.Fatalf("second agent: %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}

	const size = 256 << 20
	f, err := os.Create(filepath.Join(a.roots["tv"], "big.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	req, _ := http.NewRequest(http.MethodGet, a.base+"/v1/files?root=tv&path=big.mkv", nil)
	req.Host = mediaAgentHost
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := mediaAgentClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: %d", resp.StatusCode)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if took := a.terminate(t); took > 15*time.Second {
		t.Fatalf("stop took %s", took)
	}
	if n, _ := io.Copy(io.Discard, resp.Body); n >= size-1 {
		t.Fatalf("the in-flight download completed (%d bytes) instead of being closed", n)
	}
	if c, err := net.DialTimeout("tcp", a.addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the control port still accepts connections")
	}

	again := startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	_, body = again.get(t, "/health", nil)
	var next struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(body, &next); err != nil {
		t.Fatal(err)
	}
	if next.AgentID != first.AgentID {
		t.Fatalf("agent_id changed across restart: %s then %s", first.AgentID, next.AgentID)
	}
}

func TestMediaAgentListenerLimitsAndHangup(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	conn, err := net.DialTimeout("tcp", a.addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET /health HTTP/1.1\r\nHost: %s\r\nX-Large: %s\r\n\r\n", mediaAgentHost, strings.Repeat("x", 32<<10)); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 431 {
		t.Fatalf("oversize header status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	_ = conn.Close()
	var connections []net.Conn
	defer func() {
		for _, c := range connections {
			_ = c.Close()
		}
	}()
	for range 280 {
		c, err := net.DialTimeout("tcp", a.addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, c)
	}
	last := connections[len(connections)-1]
	if _, err := fmt.Fprintf(last, "GET /health HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", mediaAgentHost, a.token); err != nil {
		t.Fatal(err)
	}
	if err := last.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := last.Read(one[:]); err == nil {
		t.Fatal("connection cap not enforced")
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatal(err)
		}
	}
	for _, c := range connections[:len(connections)-1] {
		_ = c.Close()
	}
	if err := last.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err = http.ReadResponse(bufio.NewReader(last), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("released connection status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	_ = last.Close()
	if err := a.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.done:
	case <-time.After(15 * time.Second):
		t.Fatal("SIGHUP did not stop agent")
	}
	var exit *exec.ExitError
	if !errors.As(a.err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("SIGHUP exit=%v", a.err)
	}
}
