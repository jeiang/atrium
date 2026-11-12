package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
	"github.com/jeiang/atrium/internal/settings"
)

func wimServeIn(t *testing.T, dir string, args ...string) (string, func()) {
	t.Helper()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedAllFlags(t)
	base, _, stop, err := startServeAppArgs(t, newTestApp(t, nil), &lockedBuffer{}, args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return base, stop
}

func wimGet(t *testing.T, target string, headers ...string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestWimServeRefusesUnsafeAuthConfigurations(t *testing.T) {
	t.Parallel()
	setEnv(t, "ATRIUM_DB", filepath.Join(t.TempDir(), "atrium.db"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--auth", "proxy"}, "proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST"},
		{[]string{"--auth", "oidc"}, "oidc mode needs --allowed-host or ATRIUM_PUBLIC_HOST"},
		{[]string{"--auth", "local", "--auth", "oidc", "--allowed-host", "x"}, "local cannot combine"},
		{[]string{"--auth", "basic"}, "basic"},
		{[]string{"--host", "0.0.0.0", "--auth", "local"}, "local auth mode needs a numeric loopback --host"},
		{[]string{"--host", "::", "--auth", "local"}, "local auth mode needs a numeric loopback --host"},
		{[]string{"--host", "localhost", "--auth", "local"}, "local auth mode needs a numeric loopback --host"},
		{[]string{"--host", "192.0.2.10", "--auth", "local"}, "local auth mode needs a numeric loopback --host"},
		{[]string{"--host", "127.0.0.1", "--auth", "local", "--force-disable-auth"}, "unknown arguments: --force-disable-auth"},
	} {
		var out, errs bytes.Buffer
		code := newTestApp(t, nil).Run(context.Background(), append([]string{"serve", "--port", "1"}, c.args...), &out, &errs)
		if code == cli.ExitOK || !strings.Contains(errs.String(), c.want) {
			t.Errorf("serve %v: exit %d stderr %q", c.args, code, errs.String())
		}
	}
}

func TestWimLocalAuthServesLoopbackIPv4AndIPv6(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"127.0.0.1", "::1"} {
		base, _ := wimServeIn(t, t.TempDir(), "--host", host, "--auth", "local")
		status, body, _ := wimGet(t, base+"/settings/mail")
		if status != http.StatusOK || !strings.Contains(body, "Mail") || strings.Contains(body, "Authentication is disabled") {
			t.Fatalf("%s local mode: %d", host, status)
		}
		if status, _, _ := wimGet(t, base+"/settings/mail", "Host", "evil.example"); status != http.StatusBadRequest {
			t.Fatalf("%s local mode accepted a foreign Host: %d", host, status)
		}
		status, _, _ = wimGet(t, base+"/settings/mail", "X-Forwarded-For", "203.0.113.9", "X-Real-IP", "203.0.113.9", "CF-Connecting-IP", "203.0.113.9")
		if status != http.StatusOK {
			t.Fatalf("%s local mode with forwarded addresses: %d", host, status)
		}

		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequest(http.MethodGet, base+"/settings/mail", nil)
		if resp, err := client.Do(req); err != nil {
			t.Fatal(err)
		} else {
			_ = resp.Body.Close()
		}
		u, _ := url.Parse(base)
		post := func(token string) int {
			t.Helper()
			req, _ := http.NewRequest(http.MethodPost, base+"/settings/mail/accounts/nosuch/test", strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", base)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if code := post(""); code != http.StatusForbidden {
			t.Errorf("%s local mode without a CSRF token: %d", host, code)
		}
		token := ""
		for _, c := range jar.Cookies(u) {
			if c.Name == "atrium_csrf" {
				token = c.Value
			}
		}
		if code := post(token); code != http.StatusNotFound {
			t.Errorf("%s local mode with a CSRF token: %d", host, code)
		}
	}
}

func TestWimLocalModeRefusesANonLoopbackPeer(t *testing.T) {
	t.Parallel()
	base, _ := wimServeIn(t, t.TempDir(), "--auth", "local")
	for _, source := range nonLoopbackIPv4s() {
		transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{LocalAddr: &net.TCPAddr{IP: source}}
			return d.DialContext(ctx, network, addr)
		}}
		req, _ := http.NewRequest(http.MethodGet, base+"/settings/mail", nil)
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Real-IP", "127.0.0.1")
		req.Header.Set("CF-Connecting-IP", "127.0.0.1")
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-loopback peer %s with forwarded localhost headers: %d %.200q", source, resp.StatusCode, body)
		}
		return
	}
	t.Skip("cannot connect from a non-loopback source")
}

func nonLoopbackIPv4s() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip := ipnet.IP.To4(); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

func TestWimOIDCModeIgnoresLoopbackProxyHeaders(t *testing.T) {
	t.Parallel()
	base, _ := wimServeIn(t, t.TempDir(), "--auth", "oidc", "--allowed-host", "127.0.0.1")
	status, _, header := wimGet(t, base+"/settings/mail", "Remote-Groups", "atrium_access", "Remote-User", "root", "X-Forwarded-For", "127.0.0.1")
	if status != http.StatusSeeOther || !strings.HasPrefix(header.Get("Location"), "/login") {
		t.Fatalf("oidc mode admitted a loopback proxy request: %d %q", status, header.Get("Location"))
	}
}

func TestWimProxyAuthGatesPagesByForwardedGroups(t *testing.T) {
	t.Parallel()
	base, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--allowed-host", "127.0.0.1")
	const access, admin = "atrium_access", "atrium_access, atrium_admin"
	for _, c := range []struct {
		groups string
		status int
		want   string
	}{
		{"", http.StatusForbidden, ""},
		{"other", http.StatusForbidden, ""},
		{"atrium_admin", http.StatusForbidden, ""},
		{access, http.StatusOK, "Mail"},
		{admin, http.StatusOK, "Mail"},
	} {
		status, body, _ := wimGet(t, base+"/settings/mail", "Remote-Groups", c.groups)
		if status != c.status || !strings.Contains(body, c.want) {
			t.Errorf("groups %q: %d %.200q", c.groups, status, body)
		}
	}
	if status, _, header := wimGet(t, base+"/", "Remote-Groups", access); status != http.StatusSeeOther || header.Get("Location") == "" {
		t.Errorf("root for a member: %d %q", status, header.Get("Location"))
	}
	if status, _, _ := wimGet(t, base+"/login", "Remote-Groups", access); status != http.StatusNotFound {
		t.Errorf("proxy mode serves the oidc login page: %d", status)
	}
	if status, _, _ := wimGet(t, base+"/settings/mail", "Remote-Groups", admin, "Host", "evil.example"); status != http.StatusBadRequest {
		t.Errorf("foreign Host: %d", status)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(groups, origin string) int {
		t.Helper()
		u, _ := url.Parse(base)
		token := ""
		for _, c := range jar.Cookies(u) {
			if c.Name == "atrium_csrf" {
				token = c.Value
			}
		}
		req, _ := http.NewRequest(http.MethodPost, base+"/settings/mail/accounts/nosuch/test", strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.Header.Set("Remote-Groups", groups)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/settings/mail", nil)
	req.Header.Set("Remote-Groups", admin)
	if resp, err := client.Do(req); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
	}
	if code := post(access, base); code != http.StatusNotFound {
		t.Errorf("member POST for an unknown account: %d", code)
	}
	if code := post(admin, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin admin POST: %d", code)
	}
	if code := post(admin, base); code != http.StatusNotFound {
		t.Errorf("admin POST for an unknown account: %d", code)
	}
}

func TestWimServeMarksAStuckSendingDraftUnknownAndReportsItsLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "atrium.db")
	seed, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(settings.With(context.Background(), settings.New()), seed, path); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Write.Exec("INSERT INTO drafts (thread_id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (NULL, 'gmail', 'a@x.test', 's', 'b', 'sending', 1000, 1000)"); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	notify := sdnotifytest.Listen(t, 200*time.Millisecond)
	for name, value := range notify.Env() {
		setEnv(t, name, value)
	}
	base, stop := wimServeIn(t, dir, "--auth", "local")
	notify.Wait("READY=1")
	notify.Wait("STATUS=serving " + strings.TrimPrefix(base, "http://"))
	notify.Wait("WATCHDOG=1")

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	for _, accept := range []string{"gzip", ""} {
		req, _ := http.NewRequest(http.MethodGet, base+"/static/app.css", nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" || resp.Header.Get("Content-Encoding") != accept {
			t.Errorf("static asset with Accept-Encoding %q: Vary %v encoding %q", accept, got, resp.Header.Get("Content-Encoding"))
		}
	}

	stop()
	notify.Wait("STOPPING=1")
	after, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	var status string
	if err := after.Read.QueryRow("SELECT status FROM drafts").Scan(&status); err != nil || status != string(drafts.Unknown) {
		t.Errorf("stuck draft status = %q, %v", status, err)
	}
}
