package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntegrationClientsUseTheConnectionTransportAndAFailedRequestChecksAtOnce(t *testing.T) {
	t.Parallel()
	requireCipher(t)
	qb := &fakeQbit{version: "v5.2.0"}
	var broken atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		qb.handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", qbitHost(t, dir, srv.URL))
	s := &connectionSite{t: t, dir: dir}
	s.start()
	if state := connectionState(t, dir, "service/qbittorrent"); state == "" {
		t.Fatal("no qBittorrent connection was materialized from the host")
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil); code == http.StatusOK && qb.saw("/api/v2/torrents/info") {
		t.Fatal("the downloads page reached a server with an untrusted certificate")
	}
	waitFor(t, func() bool { return connectionState(t, dir, "service/qbittorrent") == "failing" })

	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", qbitHostWithCA(t, dir, srv.URL, ca))
	s.start()
	if code, body := s.conn("/test", url.Values{"id": {"service/qbittorrent"}, "api_key": {"qbt_good"}}); code != http.StatusOK {
		t.Fatalf("qBittorrent Test with its CA: %d %s", code, body)
	}
	if code, _, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil); code != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Fatalf("downloads page with the qBittorrent CA: %d", code)
	}
	if got := runApp(t, "connections", "test", "service/qbittorrent"); got.code != 0 || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}

	broken.Store(true)
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/downloads", nil)
	waitFor(t, func() bool { return connectionState(t, dir, "service/qbittorrent") == "failing" })
}

func TestTMDBRequestFailureReachesConnectionHealth(t *testing.T) {
	var status atomic.Int32
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := status.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"id":42,"name":"Lanterns","first_air_date":"2026-01-01","poster_path":"/lanterns.jpg"}]}`))
	}))
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
	if code, body, _ := fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns", nil); code != http.StatusOK || !strings.Contains(body, "Lanterns") {
		t.Fatalf("channels search: %d %.300s", code, body)
	}
	status.Store(http.StatusInternalServerError)
	_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+again", nil)
	waitFor(t, func() bool { return connectionState(t, dir, "service/tmdb") == "failing" })

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	observed := func() (rows, noop, foreign int) {
		t.Helper()
		if err := d.Read.QueryRow(`SELECT COUNT(*), COALESCE(SUM(outcome = 'noop'), 0), COALESCE(SUM(actor <> 'service:connection-health' OR source <> 'job'), 0)
			FROM operation_audit WHERE operation = 'connections.observe-health'`).Scan(&rows, &noop, &foreign); err != nil {
			t.Fatal(err)
		}
		return rows, noop, foreign
	}
	for i := range 5 {
		_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+"+strconv.Itoa(i), nil)
	}
	if _, err := d.Write.Exec("UPDATE connections SET state = 'ok', checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusForbidden)
	_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+forbidden", nil)
	waitFor(t, func() bool { rows, _, _ := observed(); return rows > 0 })
	rows, noop, foreign := observed()
	if rows != 1 || noop != 0 || foreign != 0 {
		t.Fatalf("observe-health audit after six failed requests and one recheck: %d rows, %d noop, %d not by the health service", rows, noop, foreign)
	}
}
