package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestAConnectionFlappingAcrossARestartIsNoticedOncePerFlapWindow(t *testing.T) {
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
	s := &connectionSite{t: t, dir: dir}
	s.start()
	waitFor(t, func() bool { return connectionState(t, dir, "service/tmdb") == "ok" })

	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() {
		if t.Failed() {
			var state, reason, results, notice string
			var checked float64
			_ = d.Read.QueryRow("SELECT state, reason, results_json, notice_json, COALESCE(checked_at,0) FROM connections WHERE id = 'service/tmdb'").Scan(&state, &reason, &results, &notice, &checked)
			t.Logf("connection row at failure: %s %s %s %s %.0f now %d", state, reason, results, notice, checked, time.Now().Unix())
		}
	})
	label, _, _ := runtimeRow(t, dir, "service/tmdb")
	before := connectionNotices(t, dir, label)
	searches := 0
	search := func() {
		t.Helper()
		searches++
		webG4(t, s.base).do(http.MethodGet, "/web/v1/media/shows?q=Lanterns+"+strconv.Itoa(searches), nil)
	}
	backdate := func() {
		t.Helper()
		if _, err := d.Write.Exec("UPDATE connections SET checked_at = ? WHERE id = 'service/tmdb'", float64(time.Now().Add(-10*time.Minute).Unix())); err != nil {
			t.Fatal(err)
		}
	}
	fail := func(what string) {
		t.Helper()
		backdate()
		status.Store(http.StatusInternalServerError)
		search()
		waitUntil(t, time.Minute, what, func() bool {
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
	}
	heal := func(what string) {
		t.Helper()
		status.Store(0)
		search()
		waitUntil(t, time.Minute, what, func() bool { return connectionState(t, dir, "service/tmdb") == "ok" })
	}

	fail("the first TMDB failure to be checked")
	waitUntil(t, time.Minute, "the failure notice", func() bool { return connectionNotices(t, dir, label)-before == 1 })
	heal("TMDB to heal")
	waitUntil(t, time.Minute, "the recovery notice", func() bool { return connectionNotices(t, dir, label)-before == 2 })

	s.start()
	fail("the TMDB failure after the restart to be checked")
	waitUntil(t, time.Minute, "the damped failure to be remembered", func() bool {
		var held int
		if err := d.Read.QueryRow("SELECT json_extract(notice_json, '$.held_at') IS NOT NULL FROM connections WHERE id = 'service/tmdb'").Scan(&held); err != nil {
			t.Fatal(err)
		}
		return held == 1
	})
	if got := connectionNotices(t, dir, label) - before; got != 2 {
		t.Fatalf("notices after a failure, a recovery, a restart and a failure inside the flap window: %d, want 2", got)
	}
}
