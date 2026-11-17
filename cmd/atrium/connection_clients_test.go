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
	// Let the start check settle and age it past the 5 minute limit, so the
	// failed request below is the one that triggers the check.
	waitFor(t, func() bool {
		state := connectionState(t, dir, "service/tmdb")
		return state != "" && state != "untested"
	})
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-10*time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusInternalServerError)
	_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+again", nil)
	waitFor(t, func() bool { return connectionState(t, dir, "service/tmdb") == "failing" })

	observed := func() (rows, noop, foreign int) {
		t.Helper()
		if err := d.Read.QueryRow(`SELECT COUNT(*), COALESCE(SUM(outcome = 'noop'), 0), COALESCE(SUM(actor <> 'service:connection-health' OR source <> 'job'), 0)
			FROM operation_audit WHERE operation = 'connections.observe-health' AND target LIKE '%service/tmdb%'`).Scan(&rows, &noop, &foreign); err != nil {
			t.Fatal(err)
		}
		return rows, noop, foreign
	}
	for i := range 5 {
		_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+"+strconv.Itoa(i), nil)
	}
	baseRows, baseNoop, _ := observed()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'ok', checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusForbidden)
	_, _, _ = fetch(t, &http.Client{}, http.MethodGet, base+"/apps/media/channels?q=Lanterns+forbidden", nil)
	waitFor(t, func() bool { rows, _, _ := observed(); return rows > baseRows })
	rows, noop, foreign := observed()
	if rows-baseRows != 1 || noop != baseNoop || foreign != 0 {
		t.Fatalf("observe-health audit after six failed requests and one recheck: %d rows, %d noop, %d not by the health service", rows-baseRows, noop-baseNoop, foreign)
	}
}

func TestAFailingConnectionIsNotRecheckedByBootOrRuntimeFailuresInsideItsBackoff(t *testing.T) {
	var status atomic.Int32
	var calls atomic.Int32
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	s := &connectionSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool {
		state := connectionState(t, dir, "service/tmdb")
		return state != "" && state != "untested"
	})

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	observed := func() int {
		t.Helper()
		var rows int
		if err := d.Read.QueryRow(`SELECT COUNT(*) FROM operation_audit WHERE operation = 'connections.observe-health' AND target LIKE '%service/tmdb%'`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := observed()
	now := time.Now()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'failing', reason = 'seeded', checked_at = ?, last_ok_at = ? WHERE id = 'service/tmdb'",
		float64(now.Add(-2*time.Minute).Unix()), float64(now.Add(-10*time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusForbidden)
	s.start()
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns+refused", nil)
	if calls.Load() == 0 {
		t.Fatal("the channels search sent nothing to TMDB")
	}

	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(now.Add(-2*time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns+after", nil)
	waitFor(t, func() bool { return observed() > before })
	if rows := observed() - before; rows != 1 {
		t.Fatalf("observe-health rows after a boot and a refused request inside the backoff and one request after it: %d, want 1", rows)
	}
	var reason string
	if err := d.Read.QueryRow("SELECT reason FROM connections WHERE id = 'service/tmdb'").Scan(&reason); err != nil || reason == "seeded" {
		t.Fatalf("the recheck after the backoff did not record: reason %q err %v", reason, err)
	}

	// Failing for 2 hours: the backoff is 3 hours from the last check. The
	// refused request left the row needing reauthorization, so the time is
	// counted from reauth_at.
	before = observed()
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ?, last_ok_at = ?, reauth_at = ? WHERE id = 'service/tmdb'",
		float64(now.Add(-2*time.Hour).Unix()), float64(now.Add(-4*time.Hour).Unix()), float64(now.Add(-4*time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns+still", nil)
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(now.Add(-3*time.Hour-time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns+later", nil)
	waitFor(t, func() bool { return observed() > before })
	if rows := observed() - before; rows != 1 {
		t.Fatalf("observe-health rows after a refused request inside a 3 hour backoff and one after it: %d, want 1", rows)
	}

	// A real request that works recovers the connection at once, inside its
	// backoff, without a check request of its own.
	before = observed()
	status.Store(0)
	if code, body, _ := fetch(t, s.client, http.MethodGet, s.base+"/apps/media/channels?q=Lanterns+back", nil); code != http.StatusOK || !strings.Contains(body, "Lanterns") {
		t.Fatalf("channels search after TMDB came back: %d %.300s", code, body)
	}
	sent := calls.Load()
	waitFor(t, func() bool { return connectionState(t, dir, "service/tmdb") == "ok" })
	if extra := calls.Load() - sent; extra != 0 || observed() != before {
		t.Fatalf("recovery sent %d check requests and %d observe-health rows, want none", extra, observed()-before)
	}
	var lastOK float64
	if err := d.Read.QueryRow("SELECT COALESCE(last_ok_at, 0) FROM connections WHERE id = 'service/tmdb'").Scan(&lastOK); err != nil || lastOK < float64(now.Unix()) {
		t.Fatalf("recovery did not record the last success: %v %v", lastOK, err)
	}
}
