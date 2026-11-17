package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func noAppChange(t *testing.T, frames <-chan appFrame, topic string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("the app event stream closed")
			}
			if f.event == "change" && f.data["topic"] == topic {
				t.Fatalf("unexpected %s change: %v", topic, f.data)
			}
		case <-deadline:
			return
		}
	}
}

func TestUsingOrTestingAConnectionWithoutAVisibleChangePublishesNoConnectionsChange(t *testing.T) {
	dir, _ := davFixture(t)
	var working atomic.Bool
	var refreshes atomic.Int64
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			refreshes.Add(1)
			if !working.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"access","token_type":"Bearer","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(google.Close)
	for name, value := range map[string]string{"client_secret": "secret", "calendar_refresh": "refresh-1"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host := filepath.Join(dir, "google-host.toml")
	body := fmt.Sprintf("[connections.\"google/default\"]\nkind = \"google\"\nlabel = \"Google\"\nconfig = { client_id = \"client-id\", token_url = %q, api_url = %q }\ncapabilities = [\"calendar\"]\ncredential_files = { \"oauth/client_secret\" = %q, \"oauth/calendar/refresh\" = %q }\n",
		google.URL, google.URL+"/", filepath.Join(dir, "client_secret"), filepath.Join(dir, "calendar_refresh"))
	if err := os.WriteFile(host, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", host)
	token := localAPIToken(t, "api")
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	s := &apiSite{t: t, base: base}
	d := asstDB(t, dir)
	row := func() (state string, checked float64, revision int64) {
		t.Helper()
		if err := d.Read.QueryRow("SELECT c.state, COALESCE(c.checked_at, 0), r.value FROM connections c, read_revisions r WHERE c.id = 'google/default' AND r.scope = 'connections'").Scan(&state, &checked, &revision); err != nil {
			t.Fatalf("google/default row: %v", err)
		}
		return state, checked, revision
	}
	settled := func() {
		t.Helper()
		_, _, last := row()
		hits := refreshes.Load()
		quiet := time.Now()
		waitUntil(t, 60*time.Second, "the connections to settle after start", func() bool {
			_, _, now := row()
			if now != last || refreshes.Load() != hits {
				last, hits, quiet = now, refreshes.Load(), time.Now()
			}
			return time.Since(quiet) >= 3*time.Second
		})
	}
	settled()
	if state, _, _ := row(); state != "needs_reauth" {
		t.Fatalf("google/default is %s after start, want needs_reauth", state)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	frames := appFrames(t, resp)
	connections := syncedRevisions(t, nextAppFrame(t, frames, "sync"))["connections"]

	_, checked, revision := row()
	hits := refreshes.Load()
	const reads = 3
	for i := range reads {
		s.do(http.MethodGet, "/api/v1/calendar/events?from=2026-11-01T00:00:00Z&to=2026-11-10T00:00:00Z", token, nil)
		state, now, rev := row()
		if state != "needs_reauth" || rev != revision {
			t.Fatalf("calendar read %d: google/default %s at connections revision %d, want needs_reauth at %d", i+1, state, rev, revision)
		}
		if now <= checked {
			t.Fatalf("calendar read %d did not record its use: checked_at %f then %f", i+1, checked, now)
		}
		checked = now
	}
	if got := s.do(http.MethodPost, "/api/v1/connections/google/default/test", token, nil); got.status != http.StatusOK {
		t.Fatalf("connection test: %d %s", got.status, got.body)
	}
	if state, now, rev := row(); state != "needs_reauth" || rev != revision || now <= checked {
		t.Fatalf("an unchanged test left google/default %s at revision %d checked %f, want needs_reauth at %d checked after %f", state, rev, now, revision, checked)
	}
	noAppChange(t, frames, "connections", 3*time.Second)
	if got := refreshes.Load() - hits; got != reads+1 {
		t.Fatalf("%d token refreshes for %d reads and one test, want %d: a fresh checked_at must not trigger an early recheck", got, reads, reads+1)
	}

	working.Store(true)
	if got := s.do(http.MethodPost, "/api/v1/connections/google/default/test", token, nil); got.status != http.StatusOK {
		t.Fatalf("connection test after the grant works: %d %s", got.status, got.body)
	}
	if state, _, rev := row(); state == "needs_reauth" || rev == revision {
		t.Fatalf("a working grant left google/default %s at revision %d", state, rev)
	}
	waitAppChange(t, frames, "connections", connections)
}
