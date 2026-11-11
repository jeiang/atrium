package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
`, qbURL, qbKey, tmdbURL, imagesURL, tmdbKey, eztvURL)
	path := filepath.Join(dir, "media-host.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
		if r.Header.Get("Authorization") != "Bearer tmdb_good" || r.URL.Path != "/3/search/tv" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"id":42,"name":"Lanterns","first_air_date":"2026-01-01","poster_path":"/lanterns.jpg"}]}`))
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
	for i := range 2 {
		code, body, _ := fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns", nil)
		if code != http.StatusOK || !strings.Contains(body, "Lanterns") {
			t.Fatalf("channels search %d: %d %.300s", i, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Errorf("two page loads opened %d TMDB connections, want 1", connections)
	}
}
