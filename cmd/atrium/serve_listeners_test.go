package main

import (
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/ops"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
)

func freeLocalPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	return port
}

func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func countOf(msgs []string, want string) int {
	n := 0
	for _, m := range msgs {
		if m == want {
			n++
		}
	}
	return n
}

func TestServeListenersShareStateButNotRateLimits(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	for name, value := range notify.Env() {
		setEnv(t, name, value)
	}
	opsBase := opsAddr(t)
	port := freeLocalPort(t)
	s := startAPISite(t, dir, api, "--emergency-port", port)
	emergency := &apiSite{t: t, base: "http://127.0.0.1:" + port}
	notify.Wait("READY=1")
	notify.Wait("WATCHDOG=1")

	browser := oidcBrowser(t, s.base, s.idp, "owner")
	code, body, _ := fetch(t, browser, http.MethodPost, s.base+"/settings/auth/tokens", url.Values{"name": {"laptop"}})
	secret := regexp.MustCompile(`atr_[A-Za-z0-9_-]{43}`).FindString(body)
	if code != http.StatusOK || secret == "" {
		t.Fatalf("issue on the public listener: %d %.300s", code, body)
	}
	for _, site := range []*apiSite{s, emergency} {
		if me := site.do(http.MethodGet, "/api/v1/me", secret, nil); me.status != http.StatusOK {
			t.Fatalf("%s: the issued token cannot read the API: %d %s", site.base, me.status, me.body)
		}
	}

	local := jarClient(t)
	code, body, _ = fetch(t, local, http.MethodGet, emergency.base+"/settings/auth", nil)
	match := regexp.MustCompile(`/settings/auth/tokens/(\d+)/revoke`).FindStringSubmatch(body)
	if code != http.StatusOK || !strings.Contains(body, "laptop") || match == nil {
		t.Fatalf("the emergency listener does not list the token issued on the public listener: %d %.400s", code, body)
	}
	if code, body, _ := fetch(t, local, http.MethodPost, emergency.base+"/settings/auth/tokens/"+match[1]+"/revoke", url.Values{}); code != http.StatusOK {
		t.Fatalf("revoke on the emergency listener: %d %.300s", code, body)
	}
	for _, site := range []*apiSite{s, emergency} {
		if refused := site.do(http.MethodGet, "/api/v1/me", secret, nil); refused.status != http.StatusUnauthorized || refused.code() != "invalid_token" {
			t.Fatalf("%s: the revoked token was admitted: %d %s", site.base, refused.status, refused.body)
		}
	}

	_, _, _ = fetch(t, local, http.MethodGet, emergency.base+"/settings/assistant", nil)
	if code, body, h := fetch(t, local, http.MethodPost, emergency.base+"/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"200000"}}); code != http.StatusSeeOther {
		t.Fatalf("save on the emergency listener: %d %q %.300s", code, h.Get("Location"), body)
	}
	if code, body, _ := fetch(t, browser, http.MethodGet, s.base+"/settings/assistant", nil); code != http.StatusOK || !strings.Contains(body, `value="200000" selected`) {
		t.Fatalf("the public listener does not show the setting saved on the emergency listener: %d %.300s", code, body)
	}

	expectAPI(t, s.do(http.MethodGet, "/api/v1/me", "", nil), http.StatusUnauthorized, "unauthenticated")
	if got := s.do(http.MethodGet, "/api/v1/me", "", nil, "Host", "evil.example"); got.status == http.StatusOK {
		t.Fatalf("the public listener accepted a foreign Host: %d", got.status)
	}
	if code, _, _ := fetch(t, jarClient(t), http.MethodGet, s.base+"/settings/auth", nil); code == http.StatusOK {
		t.Fatalf("the public listener served settings without a session")
	}
	req, err := http.NewRequest(http.MethodPost, s.base+"/settings/auth/tokens", strings.NewReader("name=csrf"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range browser.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a public token POST without a CSRF token: %d", resp.StatusCode)
	}

	limited := rateToken(t, "limited")
	allowed, _, body := rateUntilRefused(t, newRateCall(t, emergency.base), "/api/v1/me", "", limited, http.StatusOK, 130)
	if allowed < 55 || rateCode(t, body) != "rate_limited" {
		t.Fatalf("the emergency listener allowed %d before refusing: %.200s", allowed, body)
	}
	if me := s.do(http.MethodGet, "/api/v1/me", limited, nil); me.status != http.StatusOK {
		t.Fatalf("the public listener shares the emergency rate limiter: %d %s", me.status, me.body)
	}

	if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, opsBase+"/metrics", nil); status != http.StatusOK || !strings.Contains(body, "# TYPE ") {
		t.Fatalf("ops /metrics = %d", status)
	}
	for _, base := range []string{s.base, emergency.base} {
		if status, body, _ := fetch(t, http.DefaultClient, http.MethodGet, base+"/metrics", nil); status == http.StatusOK && strings.Contains(body, "# TYPE ") {
			t.Errorf("%s serves ops metrics", base)
		}
	}
	if n := countOf(notify.Messages(), "READY=1"); n != 1 {
		t.Fatalf("%d READY notifications, want 1: %q", n, notify.Messages())
	}
}

func TestServeRefusesAnEmergencyListenerOffLoopback(t *testing.T) {
	moduleFixture(t, flagsExcept("", ""))
	for _, host := range []string{"localhost", "0.0.0.0", "::", "192.0.2.10", "::ffff:127.0.0.1", "fe80::1%lo0", "127.0.0.1.example"} {
		got := runApp(t, "serve", "--port", "0", "--auth", "local", "--emergency-host", host, "--emergency-port", "0")
		if got.code != cli.ExitError || !strings.Contains(got.stderr, "numeric loopback --emergency-host") {
			t.Errorf("emergency host %q: exit %d stderr %q", host, got.code, got.stderr)
		}
	}
}

func TestServeBindCollisionsGiveNoReadyAndCloseEverySocket(t *testing.T) {
	moduleFixture(t, flagsExcept("", ""))
	for _, tc := range []struct{ name, public, emergency, ops string }{
		{"public and emergency", "a", "a", "b"},
		{"public and ops", "a", "b", "a"},
		{"emergency and ops", "c", "a", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports := map[string]string{"a": freeLocalPort(t), "b": freeLocalPort(t), "c": freeLocalPort(t)}
			notify := sdnotifytest.Listen(t, 200*time.Millisecond)
			for name, value := range notify.Env() {
				setEnv(t, name, value)
			}
			setEnv(t, settingEnv(ops.ListenKey), "127.0.0.1:"+ports[tc.ops])
			got := runApp(t, "serve", "--host", "127.0.0.1", "--port", ports[tc.public], "--auth", "local", "--emergency-port", ports[tc.emergency])
			if got.code == cli.ExitOK {
				t.Fatalf("serve with a port collision exited 0")
			}
			time.Sleep(300 * time.Millisecond)
			if slices.Contains(notify.Messages(), "READY=1") {
				t.Fatalf("READY after a bind failure: %q", notify.Messages())
			}
			for _, port := range []string{ports[tc.public], ports[tc.emergency], ports[tc.ops]} {
				l, err := net.Listen("tcp", "127.0.0.1:"+port)
				if err != nil {
					t.Fatalf("port %s is still bound after the failed serve: %v", port, err)
				}
				_ = l.Close()
			}
		})
	}
}
