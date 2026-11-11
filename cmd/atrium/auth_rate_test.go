package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
)

type rateCall struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newRateCall(t *testing.T, base string) *rateCall {
	t.Helper()
	return &rateCall{t: t, base: base, client: &http.Client{
		Transport:     &http.Transport{MaxIdleConnsPerHost: 8},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *rateCall) get(path, addr, token string) (int, http.Header, string) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if addr != "" {
		req.Header.Set("CF-Connecting-IP", addr)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func rateCode(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("not a JSON error: %v %.200q", err, body)
	}
	return envelope.Error.Code
}

func rateRetry(t *testing.T, header http.Header) int {
	t.Helper()
	raw := header.Get("Retry-After")
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q", raw)
	}
	return seconds
}

func rateUntilRefused(t *testing.T, call *rateCall, path, addr, token string, want, max int) (int, http.Header, string) {
	t.Helper()
	allowed := 0
	for range max {
		status, header, body := call.get(path, addr, token)
		if status == http.StatusTooManyRequests {
			return allowed, header, body
		}
		if status != want {
			t.Fatalf("request %d before the refusal: %d %.200s", allowed, status, body)
		}
		allowed++
	}
	t.Fatalf("no refusal within %d requests", max)
	return 0, nil, ""
}

func rateSite(t *testing.T, trusted bool) (string, string, func()) {
	t.Helper()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	args := []string{"--auth", "local"}
	if trusted {
		args = append(args, "--trusted-proxy", "127.0.0.1/32")
	}
	base, _, stop, err := startServeAppArgs(t, fixtureFor(t).app, &lockedBuffer{}, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return dir, base, stop
}

func rateToken(t *testing.T, name string) string {
	t.Helper()
	issued := runApp(t, "auth", "token", "issue", "--name", name)
	if issued.code != cli.ExitOK {
		t.Fatalf("issue %s: exit %d stderr %q", name, issued.code, issued.stderr)
	}
	return strings.TrimSpace(issued.stdout)
}

func TestAuthRateLimitsSignInAndCallbacks(t *testing.T) {
	s := wimOIDCSite(t)
	const addr = "203.0.113.10"
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, target string, form url.Values) (int, http.Header, string) {
		t.Helper()
		if !strings.HasPrefix(target, "http") {
			target = s.base + target
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, target, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("CF-Connecting-IP", addr)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", s.base)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(data)
	}
	csrf := func() string {
		u, _ := url.Parse(s.base)
		for _, c := range jar.Cookies(u) {
			if c.Name == "atrium_csrf" {
				return c.Value
			}
		}
		return ""
	}

	if status, _, _ := do(http.MethodGet, "/login", nil); status != http.StatusOK {
		t.Fatalf("login page: %d", status)
	}
	s.idp.Approve("member")
	status, header, body := do(http.MethodPost, "/login", url.Values{"csrf_token": {csrf()}, "next": {""}})
	if status != http.StatusSeeOther {
		t.Fatalf("POST /login: %d %.300s", status, body)
	}
	authz := header.Get("Location")
	status, header, body = do(http.MethodGet, authz, nil)
	if status != http.StatusFound {
		t.Fatalf("authorize: %d %.300s", status, body)
	}
	callback := header.Get("Location")

	for range 8 {
		if status, _, body := do(http.MethodGet, "/login", nil); status != http.StatusOK {
			t.Fatalf("login page before the limit: %d %.200s", status, body)
		}
	}
	status, header, body = do(http.MethodGet, "/login", nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("the eleventh sign-in request: %d %.200s", status, body)
	}
	if header.Get("Cache-Control") != "no-store" || header.Get("Content-Security-Policy") == "" {
		t.Fatalf("refusal headers: %v", header)
	}
	retry := rateRetry(t, header)

	if status, _, body := do(http.MethodPost, "/login", url.Values{"csrf_token": {csrf()}, "next": {""}}); status != http.StatusTooManyRequests {
		t.Fatalf("a refused POST /login: %d %.200s", status, body)
	}
	if n := s.count("SELECT COUNT(*) FROM auth_logins"); n != 1 {
		t.Fatalf("a refused sign-in created %d pending logins, want 1", n)
	}
	if status, _, body := do(http.MethodGet, callback, nil); status != http.StatusTooManyRequests {
		t.Fatalf("a refused callback: %d %.200s", status, body)
	}
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 0 {
		t.Fatalf("a refused callback created %d sessions", n)
	}
	if n := s.count("SELECT COUNT(*) FROM auth_logins"); n != 1 {
		t.Fatalf("a refused callback consumed its state: %d pending logins", n)
	}

	time.Sleep(time.Duration(retry)*time.Second + 100*time.Millisecond)
	if status, _, body := do(http.MethodGet, callback, nil); status != http.StatusSeeOther {
		t.Fatalf("the callback after Retry-After: %d %.200s", status, body)
	}
	if n := s.count("SELECT COUNT(*) FROM auth_sessions"); n != 1 {
		t.Fatalf("%d sessions after recovery", n)
	}
}

func TestAuthRateLimitsAPIAddressAndTokenBuckets(t *testing.T) {
	_, base, _ := rateSite(t, true)
	first, second := rateToken(t, "first"), rateToken(t, "second")
	call := newRateCall(t, base)
	const a, b = "198.51.100.10", "198.51.100.11"

	firstAllowed, header, body := rateUntilRefused(t, call, "/api/v1/me", a, first, http.StatusOK, 70)
	if firstAllowed < 60 {
		t.Fatalf("the token bucket allowed %d requests, want at least 60", firstAllowed)
	}
	if rateCode(t, body) != "rate_limited" {
		t.Fatalf("token refusal body: %.200s", body)
	}
	if header.Get("Cache-Control") != "no-store" || header.Get("Content-Security-Policy") == "" {
		t.Fatalf("refusal headers: %v", header)
	}
	retry := rateRetry(t, header)
	if status, _, body := call.get("/api/v1/me", b, first); status != http.StatusTooManyRequests {
		t.Fatalf("the same token from another source: %d %.200s", status, body)
	}

	secondAllowed, header, body := rateUntilRefused(t, call, "/api/v1/me", a, second, http.StatusOK, 70)
	if secondAllowed < 55 {
		t.Fatalf("a second token from the same source allowed %d requests", secondAllowed)
	}
	if total := firstAllowed + secondAllowed; total < 118 || total > 124 {
		t.Fatalf("the address bucket admitted %d requests across two tokens, want about 120", total)
	}
	if rateCode(t, body) != "rate_limited" {
		t.Fatalf("address refusal body: %.200s", body)
	}
	if rateRetry(t, header) != 1 {
		t.Fatalf("address Retry-After = %q", header.Get("Retry-After"))
	}

	time.Sleep(time.Duration(retry)*time.Second + 100*time.Millisecond)
	if status, _, body := call.get("/api/v1/me", a, second); status != http.StatusOK {
		t.Fatalf("a request after Retry-After: %d %.200s", status, body)
	}
}

func TestAuthRateLimitIgnoresUntrustedForwardedAddresses(t *testing.T) {
	s := newAPISite(t)
	call := newRateCall(t, s.base)
	allowed := 0
	for i := range 125 {
		status, header, body := call.get("/api/v1/me", fmt.Sprintf("198.51.100.%d", i+1), "")
		if status == http.StatusTooManyRequests {
			if rateCode(t, body) != "rate_limited" {
				t.Fatalf("refusal body: %.200s", body)
			}
			if rateRetry(t, header) < 1 {
				t.Fatalf("Retry-After = %q", header.Get("Retry-After"))
			}
			if status, _, body := call.get("/api/v1/me", "198.51.100.250", ""); status != http.StatusTooManyRequests {
				t.Fatalf("a new forged address escaped the socket bucket: %d %.200s", status, body)
			}
			if allowed < 120 {
				t.Fatalf("the socket bucket allowed %d requests, want at least 120", allowed)
			}
			return
		}
		if status != http.StatusUnauthorized {
			t.Fatalf("untrusted forwarded address %d: %d %.200s", i, status, body)
		}
		allowed++
	}
	t.Fatalf("forged forwarded addresses escaped the socket bucket")
}

func TestAuthRateLimitValidatesTrustedForwardedAddresses(t *testing.T) {
	_, base, _ := rateSite(t, true)
	call := newRateCall(t, base)
	post := func(header http.Header) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/api/v1/me", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = header
		resp, err := call.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	multi := http.Header{}
	multi.Add("CF-Connecting-IP", "198.51.100.30")
	multi.Add("CF-Connecting-IP", "198.51.100.31")
	if status, body := post(multi); status != http.StatusBadRequest || rateCode(t, body) != "bad_request" {
		t.Fatalf("two forwarded addresses: %d %.200s", status, body)
	}
	joined := http.Header{"Cf-Connecting-Ip": {"198.51.100.30, 198.51.100.31"}}
	if status, body := post(joined); status != http.StatusBadRequest || rateCode(t, body) != "bad_request" {
		t.Fatalf("a joined forwarded address: %d %.200s", status, body)
	}
	invalid := http.Header{"Cf-Connecting-Ip": {"not-an-address"}}
	if status, body := post(invalid); status != http.StatusBadRequest || rateCode(t, body) != "bad_request" {
		t.Fatalf("an invalid forwarded address: %d %.200s", status, body)
	}

	allowed, _, body := rateUntilRefused(t, call, "/api/v1/me", "198.51.100.40", "", http.StatusUnauthorized, 125)
	if allowed < 120 {
		t.Fatalf("the trusted forwarded address allowed %d requests, want at least 120", allowed)
	}
	if rateCode(t, body) != "rate_limited" {
		t.Fatalf("trusted refusal body: %.200s", body)
	}
	for i := range 121 {
		if status, _, body := call.get("/api/v1/me", fmt.Sprintf("203.0.113.%d", i+1), ""); status != http.StatusUnauthorized {
			t.Fatalf("distinct trusted address %d: %d %.200s", i, status, body)
		}
	}
}

func TestAuthRateLimitRefusesNewKeysWhenFull(t *testing.T) {
	_, base, _ := rateSite(t, true)
	call := newRateCall(t, base)
	addr := func(i int) string { return fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff) }
	for i := range 10000 {
		if status, _, body := call.get("/api/v1/me", addr(i), ""); status != http.StatusUnauthorized {
			t.Fatalf("address %d before the capacity limit: %d %.200s", i, status, body)
		}
	}
	status, header, body := call.get("/api/v1/me", addr(10000), "")
	if status != http.StatusTooManyRequests || rateCode(t, body) != "rate_limited" {
		t.Fatalf("a new key at capacity: %d %.200s", status, body)
	}
	if rateRetry(t, header) != 900 {
		t.Fatalf("capacity Retry-After = %q", header.Get("Retry-After"))
	}
	if status, _, body := call.get("/api/v1/me", addr(0), ""); status != http.StatusUnauthorized {
		t.Fatalf("a live key was dropped at capacity: %d %.200s", status, body)
	}
}

func TestAuthRateLimitKeepsLastUsedOnRefusal(t *testing.T) {
	dir, base, _ := rateSite(t, true)
	token := rateToken(t, "phone")
	call := newRateCall(t, base)
	const addr = "198.51.100.50"
	if allowed, _, body := rateUntilRefused(t, call, "/api/v1/me", addr, token, http.StatusOK, 65); allowed < 60 {
		t.Fatalf("the token bucket allowed %d requests, want at least 60: %.200s", allowed, body)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	defer func() { _ = d.Close() }()
	if _, err := d.Write.Exec("UPDATE auth_bearers SET last_used_at = 1"); err != nil {
		t.Fatal(err)
	}
	status, _, body := call.get("/api/v1/me", addr, token)
	if status != http.StatusTooManyRequests || rateCode(t, body) != "rate_limited" {
		t.Fatalf("the refused request: %d %.200s", status, body)
	}
	var lastUsed float64
	if err := d.Read.QueryRow("SELECT last_used_at FROM auth_bearers WHERE name = 'phone'").Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed != 1 {
		t.Fatalf("a refused request updated last use to %v", lastUsed)
	}
}
