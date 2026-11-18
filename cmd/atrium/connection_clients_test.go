package main

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if got := webG4(t, s.base).do(http.MethodGet, "/web/v1/media/torrents", nil); got.status == http.StatusOK && qb.saw("/api/v2/torrents/info") {
		t.Fatal("the downloads list reached a server with an untrusted certificate")
	}
	waitFor(t, func() bool { return connectionState(t, dir, "service/qbittorrent") == "failing" })

	ca := filepath.Join(dir, "qbittorrent-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", qbitHostWithCA(t, dir, srv.URL, ca))
	s.start()
	c := webG4(t, s.base)
	if got := connPutG4(c, "service/qbittorrent", "", map[string]string{"api_key": "qbt_good"}); got.status != http.StatusOK {
		t.Fatalf("qBittorrent Test with its CA: %d %s", got.status, got.body)
	}
	if got := c.do(http.MethodGet, "/web/v1/media/torrents", nil); got.status != http.StatusOK || !qb.saw("/api/v2/torrents/info") {
		t.Fatalf("downloads list with the qBittorrent CA: %d %s", got.status, got.body)
	}
	if got := runApp(t, "connections", "test", "service/qbittorrent"); got.code != 0 || !strings.Contains(got.stdout, "ok") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", got.code, got.stdout, got.stderr)
	}

	broken.Store(true)
	c.do(http.MethodGet, "/web/v1/media/torrents", nil)
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
	c := webG4(t, startServeIn(t, dir))
	if got := c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns", nil); got.status != http.StatusOK || !strings.Contains(got.body, "Lanterns") {
		t.Fatalf("show search: %d %.300s", got.status, got.body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() {
		if t.Failed() {
			var state, reason, results string
			var checked float64
			_ = d.Read.QueryRow("SELECT state, reason, results_json, COALESCE(checked_at,0) FROM connections WHERE id = 'service/tmdb'").Scan(&state, &reason, &results, &checked)
			t.Logf("connection row at failure: %s %s %s %.0f now %d", state, reason, results, checked, time.Now().Unix())
		}
	})
	observed := func() (rows, noop, foreign int) {
		t.Helper()
		if err := d.Read.QueryRow(`SELECT COUNT(*), COALESCE(SUM(outcome = 'noop'), 0), COALESCE(SUM(actor <> 'service:connection-health' OR source <> 'job'), 0)
			FROM operation_audit WHERE operation = 'connections.observe-health' AND target LIKE '%service/tmdb%'`).Scan(&rows, &noop, &foreign); err != nil {
			t.Fatal(err)
		}
		return rows, noop, foreign
	}
	waitFor(t, func() bool {
		state := connectionState(t, dir, "service/tmdb")
		return state != "" && state != "untested"
	})
	backdate := func() {
		t.Helper()
		if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-10*time.Minute).Unix())); err != nil {
			t.Fatal(err)
		}
	}
	searches := 0
	search := func() {
		searches++
		c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+again+"+strconv.Itoa(searches), nil)
	}
	backdate()
	status.Store(http.StatusInternalServerError)
	search()
	waitUntil(t, 60*time.Second, "the failed TMDB request to check the connection", func() bool {
		var state string
		var checked float64
		if err := d.Read.QueryRow("SELECT state, COALESCE(checked_at, 0) FROM connections WHERE id = 'service/tmdb'").Scan(&state, &checked); err != nil {
			t.Fatal(err)
		}
		if state == "failing" {
			return true
		}
		if time.Since(time.Unix(int64(checked), 0)) < 5*time.Minute {
			backdate()
			search()
		}
		return false
	})

	for i := range 5 {
		c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+"+strconv.Itoa(i), nil)
	}
	baseRows, baseNoop, _ := observed()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'ok', checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusForbidden)
	c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+forbidden", nil)
	waitUntil(t, 60*time.Second, "the refused TMDB request to check the connection", func() bool { rows, _, _ := observed(); return rows > baseRows })
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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	observed := func() int {
		t.Helper()
		var rows int
		if err := d.Read.QueryRow(`SELECT COUNT(*) FROM operation_audit WHERE operation = 'connections.observe-health' AND target LIKE '%service/tmdb%'`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	until := func(what string, ok func() bool) {
		t.Helper()
		started := time.Now()
		for deadline := started.Add(60 * time.Second); !ok(); time.Sleep(25 * time.Millisecond) {
			if time.Now().Before(deadline) {
				continue
			}
			var state, reason string
			var checked, lastOK, reauth *float64
			if err := d.Read.QueryRow("SELECT state, reason, checked_at, last_ok_at, reauth_at FROM connections WHERE id = 'service/tmdb'").
				Scan(&state, &reason, &checked, &lastOK, &reauth); err != nil {
				t.Fatalf("timed out after %s waiting for %s; connection row: %v", time.Since(started), what, err)
			}
			at := func(v *float64) string {
				if v == nil {
					return "never"
				}
				return fmt.Sprintf("%s ago", time.Since(time.Unix(0, int64(*v*1e9))).Round(time.Second))
			}
			backoff := "none"
			if (state == "failing" || state == "needs_reauth") && checked != nil {
				since := lastOK
				if state == "needs_reauth" && reauth != nil {
					since = reauth
				}
				tier := time.Hour
				if since != nil {
					switch failing := time.Duration((*checked - *since) * float64(time.Second)); {
					case failing >= 6*time.Hour:
						tier = 12 * time.Hour
					case failing >= 3*time.Hour:
						tier = 6 * time.Hour
					case failing >= time.Hour:
						tier = 3 * time.Hour
					}
				}
				backoff = fmt.Sprintf("%s tier, until %s", tier, time.Until(time.Unix(0, int64(*checked*1e9)).Add(tier)).Round(time.Second))
			}
			t.Fatalf("timed out after %s waiting for %s: tmdb state %q reason %q, checked %s, last ok %s, reauth %s, backoff %s; %d TMDB requests, %d observe-health rows",
				time.Since(started).Round(time.Millisecond), what, state, reason, at(checked), at(lastOK), at(reauth), backoff, calls.Load(), observed())
		}
	}
	until("the boot check to leave service/tmdb untested", func() bool {
		state := connectionState(t, dir, "service/tmdb")
		return state != "" && state != "untested"
	})

	before := observed()
	now := time.Now()
	if _, err := d.Write.Exec("UPDATE connections SET state = 'failing', reason = 'seeded', checked_at = ?, last_ok_at = ? WHERE id = 'service/tmdb'",
		float64(now.Add(-2*time.Minute).Unix()), float64(now.Add(-10*time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusForbidden)
	s.start()
	c := webG4(t, s.base)
	c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+refused", nil)
	if calls.Load() == 0 {
		t.Fatal("the channels search sent nothing to TMDB")
	}

	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(now.Add(-2*time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+after", nil)
	until("an observe-health recheck for the refused request after the 1 hour backoff", func() bool { return observed() > before })
	if rows := observed() - before; rows != 1 {
		t.Fatalf("observe-health rows after a boot and a refused request inside the backoff and one request after it: %d, want 1", rows)
	}
	var reason string
	if err := d.Read.QueryRow("SELECT reason FROM connections WHERE id = 'service/tmdb'").Scan(&reason); err != nil || reason == "seeded" {
		t.Fatalf("the recheck after the backoff did not record: reason %q err %v", reason, err)
	}

	before = observed()
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ?, last_ok_at = ?, reauth_at = ? WHERE id = 'service/tmdb'",
		float64(now.Add(-2*time.Hour).Unix()), float64(now.Add(-4*time.Hour).Unix()), float64(now.Add(-4*time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+still", nil)
	if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(now.Add(-3*time.Hour-time.Minute).Unix())); err != nil {
		t.Fatal(err)
	}
	c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+later", nil)
	until("an observe-health recheck for the refused request after the 3 hour backoff", func() bool { return observed() > before })
	if rows := observed() - before; rows != 1 {
		t.Fatalf("observe-health rows after a refused request inside a 3 hour backoff and one after it: %d, want 1", rows)
	}

	before = observed()
	status.Store(0)
	if got := c.do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+back", nil); got.status != http.StatusOK || !strings.Contains(got.body, "Lanterns") {
		t.Fatalf("show search after TMDB came back: %d %.300s", got.status, got.body)
	}
	sent := calls.Load()
	until("the successful search to mark service/tmdb ok without a check", func() bool { return connectionState(t, dir, "service/tmdb") == "ok" })
	if extra := calls.Load() - sent; extra != 0 || observed() != before {
		t.Fatalf("recovery sent %d check requests and %d observe-health rows, want none", extra, observed()-before)
	}
	var lastOK float64
	if err := d.Read.QueryRow("SELECT COALESCE(last_ok_at, 0) FROM connections WHERE id = 'service/tmdb'").Scan(&lastOK); err != nil || lastOK < float64(now.Unix()) {
		t.Fatalf("recovery did not record the last success: %v %v", lastOK, err)
	}
}
