package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func mediaHostConfig(t *testing.T, dir, qbURL, tmdbURL, imagesURL, eztvURL string) string {
	t.Helper()
	qbKey := filepath.Join(dir, "qbit.key")
	tmdbKey := filepath.Join(dir, "tmdb.key")
	if err := os.WriteFile(qbKey, []byte("qbt_good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmdbKey, []byte("tmdb_good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[connections."service/qbittorrent"]
kind = "service/qbittorrent"
label = "qBittorrent"
config = { url = %q }
capabilities = ["api"]
credential_files = { api_key = %q }

[connections."service/tmdb"]
kind = "service/tmdb"
label = "TMDB"
config = { url = %q, images_url = %q }
capabilities = ["api", "images"]
credential_files = { read_token = %q }

[connections."service/eztv"]
kind = "service/eztv"
label = "EZTV"
config = { url = %q }
capabilities = ["indexer"]
`, qbURL, qbKey, tmdbURL, imagesURL, tmdbKey, eztvURL) + defaultAgentConnection
	path := filepath.Join(dir, "media-host.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQbitConnectionNeedsItsAPIKey(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	keyless := 0
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer qbt_good" {
			mu.Lock()
			if r.Header.Get("Authorization") == "" {
				keyless++
			}
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.3.0"))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"hash":"` + strings.Repeat("a", 40) + `","name":"Lanterns S01E01","state":"downloading","tags":"media","progress":0.5}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(qb.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	good, err := os.ReadFile(mediaHostConfig(t, dir, qb.URL, "http://tmdb.invalid/3", "http://images.invalid", "http://eztv.invalid/api/get-torrents"))
	if err != nil {
		t.Fatal(err)
	}
	keyLine := "credential_files = { api_key = " + strconv.Quote(filepath.Join(dir, "qbit.key")) + " }\n"
	if !strings.Contains(string(good), keyLine) {
		t.Fatalf("the host config has no qBittorrent key line: %s", good)
	}
	wrongKey := "qbt_" + strings.Repeat("w", 28)
	wrongLine := "credential_files = { api_key = " + strconv.Quote(writeFile(t, dir, "wrong.key", wrongKey)) + " }\n"
	keylessHost := writeFile(t, dir, "keyless.toml", strings.Replace(string(good), keyLine, "", 1))
	wrongHost := writeFile(t, dir, "wrong.toml", strings.Replace(string(good), keyLine, wrongLine, 1))
	goodHost := writeFile(t, dir, "good.toml", string(good))

	setEnv(t, "ATRIUM_HOST_CONFIG", keylessHost)
	ran := runApp(t, "connections", "test", "service/qbittorrent", "--json")
	if !strings.Contains(ran.stdout, `"state": "needs_reauth"`) || !strings.Contains(ran.stdout, "qBittorrent requires an API key") {
		t.Fatalf("keyless connection test = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	mu.Lock()
	refused := keyless
	mu.Unlock()
	if refused == 0 {
		t.Fatal("the keyless test never reached qBittorrent")
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", wrongHost)
	ran = runApp(t, "connections", "test", "service/qbittorrent", "--json")
	if !strings.Contains(ran.stdout, `"state": "needs_reauth"`) || !strings.Contains(ran.stdout, "qBittorrent refused the API key") || strings.Contains(ran.stdout+ran.stderr, wrongKey) {
		t.Fatalf("wrong-key connection test = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}

	setEnv(t, "ATRIUM_HOST_CONFIG", goodHost)
	ran = runApp(t, "connections", "test", "service/qbittorrent", "--json")
	if ran.code != cli.ExitOK || !strings.Contains(ran.stdout, `"state": "ok"`) || strings.Contains(ran.stdout+ran.stderr, "qbt_good") {
		t.Fatalf("keyed connection test = %d %s %s", ran.code, ran.stdout, ran.stderr)
	}
	base, logs := startServeLogged(t, dir)
	if got := localWebG1(t, base).do(http.MethodGet, "/web/v1/media/torrents", nil); got.status != http.StatusOK || !strings.Contains(got.body, "Lanterns S01E01") {
		t.Fatalf("downloads with the host key: %d %.300s", got.status, got.body)
	}
	if strings.Contains(logs.String(), "qbt_good") {
		t.Error("the serve log carried the qBittorrent key")
	}
}

func TestQbitRefusalMarksTheConnection(t *testing.T) {
	t.Parallel()
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(qb.Close)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHostConfig(t, dir, qb.URL, "http://tmdb.invalid/3", "http://images.invalid", "http://eztv.invalid/api/get-torrents"))
	base, logs := startServeLogged(t, dir)
	if got := localWebG1(t, base).do(http.MethodGet, "/web/v1/media/torrents", nil); got.status == http.StatusInternalServerError {
		t.Fatalf("downloads with a refused key: %d %.300s", got.status, got.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	waitFor(t, func() bool {
		var state, reason string
		if err := d.Read.QueryRow("SELECT state, reason FROM connections WHERE id = 'service/qbittorrent'").Scan(&state, &reason); err != nil {
			return false
		}
		return state == "needs_reauth" && strings.Contains(reason, "qBittorrent refused the API key")
	})
	if strings.Contains(logs.String(), "qbt_good") {
		t.Error("the serve log carried the qBittorrent key")
	}
}

func TestMediaConnectionsTestThroughTheCLI(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var qbitVersion, tmdbAuth, eztvPath string
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		qbitVersion = r.URL.Path
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer qbt_good" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("v5.2.0"))
	}))
	t.Cleanup(qb.Close)
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tmdbAuth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tmdb_good" || r.URL.Path != "/3/configuration" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(tmdb.Close)
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	t.Cleanup(images.Close)
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		eztvPath = r.URL.Path + "?" + r.URL.RawQuery
		mu.Unlock()
		_, _ = w.Write([]byte(`{"torrents":[]}`))
	}))
	t.Cleanup(eztv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHostConfig(t, dir, qb.URL, tmdb.URL+"/3", images.URL, eztv.URL+"/api/get-torrents"))
	for _, id := range []string{"service/qbittorrent", "service/tmdb", "service/eztv"} {
		ran := runApp(t, "connections", "test", id, "--json")
		if ran.code != cli.ExitOK {
			t.Fatalf("connections test %s: exit %d stdout %q stderr %q", id, ran.code, ran.stdout, ran.stderr)
		}
		if !strings.Contains(ran.stdout, `"state": "ok"`) {
			t.Errorf("connections test %s did not report ok: %s", id, ran.stdout)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if qbitVersion != "/api/v2/app/version" {
		t.Errorf("the qBittorrent test asked %q", qbitVersion)
	}
	if tmdbAuth != "Bearer tmdb_good" {
		t.Errorf("the TMDB test used %q", tmdbAuth)
	}
	if !strings.HasPrefix(eztvPath, "/api/get-torrents?") || !strings.Contains(eztvPath, "limit=1") {
		t.Errorf("the EZTV test asked %q", eztvPath)
	}
}

func TestMediaPagesReuseTheConnectionClient(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	connections := 0
	tmdb := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tmdb_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/configuration":
			_, _ = w.Write([]byte(`{}`))
		case "/3/search/tv":
			_, _ = w.Write([]byte(`{"results":[{"id":42,"name":"Lanterns","first_air_date":"2026-01-01","poster_path":"/lanterns.jpg"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	tmdb.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		mu.Lock()
		connections++
		mu.Unlock()
	}
	tmdb.Start()
	t.Cleanup(tmdb.Close)
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	t.Cleanup(images.Close)
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"torrents":[]}`))
	}))
	t.Cleanup(eztv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHostConfig(t, dir, "http://qbittorrent.invalid", tmdb.URL+"/3", images.URL, eztv.URL+"/api/get-torrents"))
	base := startServeIn(t, dir)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	waitFor(t, func() bool {
		var state string
		return d.Read.QueryRow("SELECT state FROM connections WHERE id = 'service/tmdb' AND checked_at IS NOT NULL").Scan(&state) == nil && state == "ok"
	})
	web := localWebG1(t, base)
	for i := range 2 {
		if got := web.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns", nil); got.status != http.StatusOK || !strings.Contains(got.body, "Lanterns") {
			t.Fatalf("channels search %d: %d %.300s", i, got.status, got.body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Errorf("two searches opened %d TMDB connections, want 1", connections)
	}
}
