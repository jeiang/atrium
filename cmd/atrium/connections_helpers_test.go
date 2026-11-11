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
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
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
