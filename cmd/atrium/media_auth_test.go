package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/media"
)

type fakeQbit struct {
	mu      sync.Mutex
	version string
	calls   []string
}

func (f *fakeQbit) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer qbt_good" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.calls = append(f.calls, r.URL.Path)
	switch r.URL.Path {
	case "/api/v2/app/version":
		_, _ = w.Write([]byte(f.version))
	case "/api/v2/torrents/info":
		_, _ = w.Write([]byte("[]"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeQbit) set(version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = version
}

func (f *fakeQbit) saw(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == path {
			return true
		}
	}
	return false
}

func TestMediaCredentialsTestThroughIntegrationsAndAuthenticateEveryRequest(t *testing.T) {
	qb := &fakeQbit{version: "v5.2.0"}
	qbSrv := httptest.NewServer(http.HandlerFunc(qb.handler))
	t.Cleanup(qbSrv.Close)
	var tmdbQuery []string
	var tmdbMu sync.Mutex
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmdbMu.Lock()
		tmdbQuery = append(tmdbQuery, r.URL.RawQuery)
		tmdbMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tmdb_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(tmdb.Close)
	previous := media.TMDBAPI
	media.TMDBAPI = tmdb.URL + "/3"
	t.Cleanup(func() { media.TMDBAPI = previous })

	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv(settingEnv(media.KeyQbitURL), qbSrv.URL)
	t.Setenv(settingEnv(media.KeyQbitAPIKey), "qbt_good")
	t.Setenv(settingEnv(media.KeyTMDBToken), "tmdb_good")
	s := &integrationSite{t: t, dir: dir}
	s.start()

	if code, body := s.post("/qbittorrent/test", nil); code != http.StatusOK || health(t, dir, "qbittorrent") != integrations.OK {
		t.Fatalf("qBittorrent 5.2 with its key: %d %s, state %s", code, body, health(t, dir, "qbittorrent"))
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/media/downloads", nil); code != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Errorf("downloads page did not list torrents with the key: %d", code)
	}
	if code, body := s.post("/tmdb/test", nil); code != http.StatusOK || health(t, dir, "tmdb") != integrations.OK {
		t.Fatalf("TMDB with its token: %d %s", code, body)
	}
	tmdbMu.Lock()
	for _, q := range tmdbQuery {
		if strings.Contains(q, "api_key") {
			t.Errorf("TMDB request carried the key in its query: %q", q)
		}
	}
	tmdbMu.Unlock()

	qb.set("v5.1.2")
	if code, body := s.post("/qbittorrent/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "has no API keys; upgrade to 5.2") {
		t.Errorf("an API key on qBittorrent 5.1 passed Test: %d", code)
	}
	if health(t, dir, "qbittorrent") != integrations.Failing {
		t.Errorf("qBittorrent 5.1 with a key: state %s, want failing", health(t, dir, "qbittorrent"))
	}

	t.Setenv(settingEnv(media.KeyTMDBToken), "tmdb_wrong")
	s.start()
	if code, _ := s.post("/tmdb/test", nil); code != http.StatusUnprocessableEntity || health(t, dir, "tmdb") != integrations.NeedsReauth {
		t.Errorf("a refused TMDB token: %d, state %s, want needs_reauth", code, health(t, dir, "tmdb"))
	}
}
