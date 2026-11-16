package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

func TestReviewHubServesOtherUnitsWhenMailIsOff(t *testing.T) {
	s := newStreamSite(t)
	exec := func(query string) {
		t.Helper()
		if _, err := s.d.Write.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (900, 'permission', 'pending', 50, 'Allow web_fetch', 'fp-permission')")
	exec("INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (1, 'a', 'pat@person.test', 'Re: lease', '', 'draft', 200, 200)")

	get := func(path string) apiReply {
		t.Helper()
		browser := newWebClient(t, s.base, nil)
		if strings.HasPrefix(path, "/api/") {
			return s.do(http.MethodGet, path, s.token, nil)
		}
		browser.session("local")
		return browser.do(http.MethodGet, path, nil)
	}
	expect := func(path string, status int, has, lacks []string) {
		t.Helper()
		got := get(path)
		if got.status != status {
			t.Fatalf("%s: status %d, want %d: %s", path, got.status, status, got.body)
		}
		for _, text := range has {
			if !strings.Contains(string(got.body), text) {
				t.Fatalf("%s lacks %q", path, text)
			}
		}
		for _, text := range lacks {
			if strings.Contains(string(got.body), text) {
				t.Fatalf("%s shows %q", path, text)
			}
		}
	}

	expect("/review", http.StatusOK, []string{"Allow web_fetch", "Re: lease"}, nil)
	expect("/activity?category=mail", http.StatusOK, nil, nil)
	expect("/api/v1/pending", http.StatusOK, []string{"Re: lease"}, nil)

	seedSettings(t, map[string]string{module.FlagKey(unit.Mail): "false"})
	s.restart()

	expect("/review", http.StatusOK, []string{"Allow web_fetch"}, []string{"Re: lease"})
	expect("/proposals/900", http.StatusOK, []string{"Allow web_fetch"}, nil)
	expect("/activity", http.StatusOK, nil, []string{`value="mail"`})
	expect("/activity/recent", http.StatusOK, nil, nil)
	if got := get("/activity?category=mail"); got.status == http.StatusOK {
		t.Fatalf("mail is off but the mail activity category is served: %s", got.body)
	}
	expect("/api/v1/pending", http.StatusOK, []string{`"drafts":[]`}, []string{"Re: lease"})
}
