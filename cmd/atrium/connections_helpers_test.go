package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

func highPriority(t *testing.T, push *fakePush, browser pushBrowser) []string {
	t.Helper()
	push.mu.Lock()
	received := slices.Clone(push.received)
	push.mu.Unlock()
	var out []string
	for _, r := range received {
		if r.header.Get("Urgency") == "high" {
			out = append(out, fmt.Sprint(browser.decrypt(t, r.body)["title"]))
		}
	}
	return out
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if f := fixtureFor(t); f != nil {
			f.unset(name)
		}
		setEnv(t, name, "")
	}
}

type connectionSite struct {
	t      *testing.T
	dir    string
	base   string
	client *http.Client
	stop   func()
}

func (s *connectionSite) start() {
	s.t.Helper()
	if s.stop != nil {
		s.stop()
	}
	s.base, _, s.stop = startServeStoppable(s.t, s.dir)
	jar, _ := cookiejar.New(nil)
	s.client = &http.Client{Jar: jar}
}

func (s *connectionSite) page() string {
	s.t.Helper()
	code, body, _ := fetch(s.t, s.client, http.MethodGet, s.base+"/settings/connections", nil)
	if code != http.StatusOK {
		s.t.Fatalf("connections page: %d", code)
	}
	return body
}

func (s *connectionSite) conn(path string, form url.Values) (int, string) {
	s.t.Helper()
	s.page()
	if form == nil {
		form = url.Values{}
	}
	code, body, _ := fetch(s.t, s.client, http.MethodPost, s.base+"/settings/connections"+path, form)
	return code, body
}

func connectionState(t *testing.T, dir, id string) string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	var state string
	if err := d.Read.QueryRow("SELECT state FROM connections WHERE id = ?", id).Scan(&state); err != nil {
		return ""
	}
	return state
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not reached")
}

func settleBootChecks(t *testing.T, dir string, key db.Key) {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), key)
	defer func() { _ = d.Close() }()
	count := func(query string) int64 {
		var n int64
		if err := d.Read.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && count("SELECT COUNT(*) FROM connections WHERE enabled = 1 AND checked_at IS NULL") > 0; time.Sleep(50 * time.Millisecond) {
	}
	revision := "SELECT COALESCE(MAX(value), 0) FROM read_revisions WHERE scope = 'connections'"
	for last, still := count(revision), 0; still < 5; time.Sleep(200 * time.Millisecond) {
		if now := count(revision); now == last {
			still++
		} else {
			last, still = now, 0
		}
	}
}
