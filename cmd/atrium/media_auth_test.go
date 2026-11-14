package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
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

type fakeTMDB struct {
	mu      sync.Mutex
	queries []string
	tokens  []string
}

func (f *fakeTMDB) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.RawQuery)
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") != "Bearer tmdb_good" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/3/configuration":
		_, _ = w.Write([]byte(`{}`))
	case "/3/search/tv":
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"id": 42, "name": "Lanterns", "first_air_date": "2026-01-01", "poster_path": "/lanterns.jpg"},
		}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTMDB) snapshot() (queries, tokens []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...), append([]string(nil), f.tokens...)
}

func TestMediaCredentialsTestThroughConnectionsAndAuthenticateEveryRequest(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	qb := &fakeQbit{version: "v5.2.0"}
	qbSrv := httptest.NewServer(http.HandlerFunc(qb.handler))
	t.Cleanup(qbSrv.Close)
	tmdb := &fakeTMDB{}
	tmdbSrv := httptest.NewServer(http.HandlerFunc(tmdb.handler))
	t.Cleanup(tmdbSrv.Close)
	var imageMu sync.Mutex
	var imageAuth []string
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageMu.Lock()
		imageAuth = append(imageAuth, r.Header.Get("Authorization"))
		imageMu.Unlock()
		_, _ = w.Write([]byte("image"))
	}))
	t.Cleanup(images.Close)
	eztv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"torrents":[]}`))
	}))
	t.Cleanup(eztv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, qbSrv.URL, eztv.URL, tmdbSrv.URL+"/3", images.URL))
	s := &connectionSite{t: t, dir: dir}
	s.start()

	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "url": {qbSrv.URL}, "api_key": {"qbt_good"}}); code != http.StatusOK || connectionState(t, dir, "service/qbittorrent") != "ok" {
		t.Fatalf("qBittorrent 5.2 with its key: %d %s, state %s", code, body, connectionState(t, dir, "service/qbittorrent"))
	}
	if code, body := s.conn("/test", url.Values{"id": {"service/tmdb"}, "credential.read_token": {"tmdb_good"}}); code != http.StatusOK || connectionState(t, dir, "service/tmdb") != "ok" {
		t.Fatalf("TMDB with its read token: %d %s, state %s", code, body, connectionState(t, dir, "service/tmdb"))
	}
	if code, body := s.conn("/test", url.Values{"id": {"service/eztv"}}); code != http.StatusOK || connectionState(t, dir, "service/eztv") != "ok" {
		t.Fatalf("EZTV with its indexer URL: %d %s, state %s", code, body, connectionState(t, dir, "service/eztv"))
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil); code != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Errorf("downloads page did not list torrents with the key: %d", code)
	}
	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns", nil); code != http.StatusOK || !strings.Contains(body, "Lanterns") {
		t.Errorf("channels search did not use the stored TMDB token: %d %.300s", code, body)
	}
	queries, tokens := tmdb.snapshot()
	if len(tokens) == 0 {
		t.Fatal("the channels page never reached the TMDB fake")
	}
	for i, token := range tokens {
		if token != "Bearer tmdb_good" {
			t.Errorf("TMDB request %d used %q", i, token)
		}
	}
	for _, q := range queries {
		if strings.Contains(q, "api_key") {
			t.Errorf("TMDB request carried the key in its query: %q", q)
		}
	}
	imageMu.Lock()
	for _, auth := range imageAuth {
		if auth != "" {
			t.Errorf("the image host received %q", auth)
		}
	}
	imageMu.Unlock()

	qb.set("v5.1.2")
	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "url": {qbSrv.URL}, "api_key": {"qbt_good"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "has no API keys; upgrade to 5.2") {
		t.Errorf("an API key on qBittorrent 5.1 passed Test: %d", code)
	}
	if connectionState(t, dir, "service/qbittorrent") != "ok" {
		t.Errorf("a failed candidate replaced the working connection: state %s", connectionState(t, dir, "service/qbittorrent"))
	}

	if code, _ := s.conn("/test", url.Values{"id": {"service/tmdb"}, "credential.read_token": {"tmdb_wrong"}}); code != http.StatusUnprocessableEntity {
		t.Errorf("a refused TMDB token passed Test: %d", code)
	}
	if state := connectionState(t, dir, "service/tmdb"); state != "ok" {
		t.Errorf("a refused TMDB token replaced the working connection: state %s", state)
	}
	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns", nil); code != http.StatusOK || !strings.Contains(body, "Lanterns") {
		t.Errorf("the stored TMDB token stopped working after a refused candidate: %d %.300s", code, body)
	}
}
