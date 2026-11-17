package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail/drafts"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/sdnotify/sdnotifytest"
	"github.com/jeiang/atrium/internal/settings"
)

func wimServeIn(t *testing.T, dir string, args ...string) (string, func()) {
	t.Helper()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedAllFlags(t)
	base, _, stop, err := startServeAppArgs(t, newTestApp(t), &lockedBuffer{}, args...)
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
		code := newTestApp(t).Run(context.Background(), append([]string{"serve", "--port", "1"}, c.args...), &out, &errs)
		if code == cli.ExitOK || !strings.Contains(errs.String(), c.want) {
			t.Errorf("serve %v: exit %d stderr %q", c.args, code, errs.String())
		}
	}
}

func TestWimLocalAuthServesLoopbackIPv4AndIPv6(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"127.0.0.1", "::1"} {
		base, _ := wimServeIn(t, t.TempDir(), "--host", host, "--auth", "local")
		status, body, _ := wimMeG5(t, base)
		if status != http.StatusOK {
			t.Fatalf("%s local mode: %d %.200q", host, status, body)
		}
		if status, _, _ := wimMeG5(t, base, "Host", "evil.example"); status != http.StatusBadRequest {
			t.Fatalf("%s local mode accepted a foreign Host: %d", host, status)
		}
		status, _, _ = wimMeG5(t, base, "X-Forwarded-For", "203.0.113.9", "X-Real-IP", "203.0.113.9", "CF-Connecting-IP", "203.0.113.9")
		if status != http.StatusOK {
			t.Fatalf("%s local mode with forwarded addresses: %d", host, status)
		}

		c := newWebClient(t, base, nil)
		c.session("local")
		if code := c.do(http.MethodPost, "/web/v1/mail/accounts/nosuch/test", nil, "X-CSRF-Token", "").status; code != http.StatusForbidden {
			t.Errorf("%s local mode without a CSRF token: %d", host, code)
		}
		if code := c.do(http.MethodPost, "/web/v1/mail/accounts/nosuch/test", nil).status; code != http.StatusNotFound {
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
	status, body, _ := wimMeG5(t, base, "Remote-Groups", "atrium_access", "Remote-User", "root", "X-Forwarded-For", "127.0.0.1")
	if status != http.StatusUnauthorized || !strings.HasPrefix(wimLoginURLG5(t, body), "/login") {
		t.Fatalf("oidc mode admitted a loopback proxy request: %d %.200q", status, body)
	}
}

func TestWimProxyAuthGatesPagesByForwardedGroups(t *testing.T) {
	t.Parallel()
	base, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--allowed-host", "127.0.0.1", "--trusted-proxy", "127.0.0.1/32")
	const access, admin = "atrium_access", "atrium_access, atrium_admin"
	for _, c := range []struct {
		groups string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"other", http.StatusUnauthorized},
		{"atrium_admin", http.StatusUnauthorized},
		{access, http.StatusOK},
		{admin, http.StatusOK},
	} {
		if status, body, _ := wimMeG5(t, base, "Remote-Groups", c.groups); status != c.status {
			t.Errorf("groups %q: %d %.200q", c.groups, status, body)
		}
	}
	if status, _, _ := wimGet(t, base+"/login", "Remote-Groups", access); status != http.StatusNotFound {
		t.Errorf("proxy mode serves the oidc login page: %d", status)
	}
	if status, _, _ := wimMeG5(t, base, "Remote-Groups", admin, "Host", "evil.example"); status != http.StatusBadRequest {
		t.Errorf("foreign Host: %d", status)
	}

	c := newWebClient(t, base, nil)
	sess := c.do(http.MethodGet, "/web/v1/session", nil, "Remote-Groups", admin)
	c.csrf, _ = sess.json["csrf_token"].(string)
	if sess.status != http.StatusOK || sess.json["auth_mode"] != "proxy" || c.csrf == "" {
		t.Fatalf("proxy session: %d %s", sess.status, sess.body)
	}
	post := func(groups, origin string) int {
		t.Helper()
		return c.do(http.MethodPost, "/web/v1/mail/accounts/nosuch/test", nil, "Remote-Groups", groups, "Origin", origin).status
	}
	if code := post(access, base); code != http.StatusNotFound {
		t.Errorf("member POST for an unknown account: %d", code)
	}
	if code := post(admin, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin admin POST: %d", code)
	}
	if code := post("other", base); code != http.StatusUnauthorized {
		t.Errorf("POST without the access group: %d", code)
	}
	if code := post(admin, base); code != http.StatusNotFound {
		t.Errorf("admin POST for an unknown account: %d", code)
	}
}

func TestWimProxyIdentityCountsOnlyFromTrustedPeers(t *testing.T) {
	t.Parallel()
	forged := []string{"Remote-Groups", "atrium_access, atrium_admin", "Remote-User", "root"}
	untrusted, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--allowed-host", "127.0.0.1", "--trusted-proxy", "192.0.2.0/24")
	if status, body, _ := wimMeG5(t, untrusted, forged...); status != http.StatusUnauthorized {
		t.Errorf("proxy mode admitted identity headers from an untrusted peer: %d %.200q", status, body)
	}
	combined, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--auth", "oidc", "--allowed-host", "127.0.0.1", "--trusted-proxy", "192.0.2.0/24")
	if status, body, _ := wimMeG5(t, combined, forged...); status != http.StatusUnauthorized || !strings.HasPrefix(wimLoginURLG5(t, body), "/login") {
		t.Errorf("proxy+oidc mode admitted identity headers from an untrusted peer: %d %.200q", status, body)
	}
	trusted, _ := wimServeIn(t, t.TempDir(), "--auth", "proxy", "--allowed-host", "127.0.0.1", "--trusted-proxy", "192.0.2.0/24", "--trusted-proxy", "127.0.0.1/32")
	if status, body, _ := wimMeG5(t, trusted, forged...); status != http.StatusOK {
		t.Errorf("proxy mode refused identity headers from a trusted peer: %d %.200q", status, body)
	}
	if status, _, _ := wimMeG5(t, trusted); status != http.StatusUnauthorized {
		t.Errorf("proxy mode admitted a trusted peer without identity headers: %d", status)
	}
}

func TestWimProxyModeRefusesToStartWithoutATrustedProxy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_PUBLIC_HOST", "")
	seedAllFlags(t)
	for _, auth := range [][]string{{"--auth", "proxy"}, {"--auth", "proxy", "--auth", "oidc"}} {
		var out, errs bytes.Buffer
		code := newTestApp(t).Run(context.Background(), append([]string{"serve", "--port", "0", "--allowed-host", "127.0.0.1"}, auth...), &out, &errs)
		if code != cli.ExitConfig || !strings.Contains(errs.String(), "proxy mode needs --trusted-proxy or host web.trusted_proxy_cidrs") {
			t.Errorf("serve %v without a trusted proxy: exit %d stderr %q", auth, code, errs.String())
		}
	}
	setEnv(t, "ATRIUM_WEB_TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
	base, _ := wimServeIn(t, dir, "--auth", "proxy", "--allowed-host", "127.0.0.1")
	if status, body, _ := wimMeG5(t, base, "Remote-Groups", "atrium_access"); status != http.StatusOK {
		t.Errorf("proxy mode refused identity headers from the host's trusted proxy: %d %.200q", status, body)
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
	if err := db.Migrate(settings.With(context.Background(), testSettings()), seed, path); err != nil {
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
