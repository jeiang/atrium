package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/settings"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func consentURL(port string) string {
	return "https://accounts.google.com/o/oauth2/v2/auth?client_id=golden-google-client-id&redirect_uri=" +
		url.QueryEscape("http://127.0.0.1:"+port) +
		"&response_type=code&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcalendar.events&access_type=offline&prompt=consent" +
		"&state=golden-token-urlsafe-16&code_challenge=0rnfdO8zLHbPYynUP5e9BxvYBwHL12yVVJTRDlfzAis&code_challenge_method=S256"
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func noTools() (*registry.Registry, error) { return registry.New(settings.New()), nil }

func pinnedTokens(n int) string { return fmt.Sprintf("golden-token-urlsafe-%d", n) }

func googleAuthApp(t *testing.T) *cli.App {
	reg := settings.New()
	app := cli.New("atrium", quietLog)
	app.Settings = reg
	app.State = config.NewState(reg)
	app.Bootstrap = bootstrap(testLookupFor(t), reg)
	app.Add(calendarcmd.Command(testLookupFor(t), noTools, calendar.AuthOptions{Random: pinnedTokens}))
	return app
}

type authRun struct {
	code           int
	stdout, stderr string
}

func runGoogleAuth(t *testing.T, redirect, port string, args ...string) authRun {
	t.Helper()
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_CONFIG", "")
	done := make(chan authRun, 1)
	go func() {
		var out, errs bytes.Buffer
		code := googleAuthApp(t).Run(context.Background(), append([]string{"calendar", "google-auth", "--port", port}, args...), &out, &errs)
		done <- authRun{code, out.String(), errs.String()}
	}()
	if redirect != "" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := http.Get("http://127.0.0.1:" + port + "/" + redirect)
			if err == nil {
				_ = resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("loopback server never answered: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	select {
	case run := <-done:
		return run
	case <-time.After(20 * time.Second):
		t.Fatal("calendar google-auth did not finish")
		return authRun{}
	}
}

func TestGoogleAuthGolden(t *testing.T) {
	setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "golden-google-client-id")
	setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "golden-google-client-secret")
	srv := golden.Replay(t, golden.LoadScenario(t, "google_auth", "code_exchange").Exchanges)
	setEnv(t, calendar.GoogleTokenEnv, srv.URL+"/token")
	port := freePort(t)
	run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=golden-google-code&scope=calendar", port)
	want := "Open this URL in a browser and approve access:\n\n" + consentURL(port) + "\n\n\nGOOGLE_REFRESH_TOKEN=golden-google-refresh-token\n"
	if run.code != cli.ExitOK || run.stdout != want || run.stderr != "" {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %q", run.code, run.stdout, want, run.stderr)
	}
}

func TestGoogleAuthRefusals(t *testing.T) {
	t.Run("client not set", func(t *testing.T) {
		setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "")
		setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "")
		run := runGoogleAuth(t, "", freePort(t))
		if run.code != cli.ExitError || run.stdout != "" || run.stderr != "error: ATRIUM_CALENDAR_GOOGLE_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("provider error", func(t *testing.T) {
		setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
			run := runGoogleAuth(t, "?error=access_denied", freePort(t))
		if run.code != cli.ExitError || run.stderr != "error: Google returned an error: access_denied\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
			run := runGoogleAuth(t, "?state=other&code=x", freePort(t))
		if run.code != cli.ExitError || run.stderr != "error: state mismatch; start again\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("token exchange failed", func(t *testing.T) {
		setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
			srv := httptestServer(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
		setEnv(t, calendar.GoogleTokenEnv, srv)
		run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=x", freePort(t))
		if run.code != cli.ExitError || !strings.HasPrefix(run.stderr, "error: token exchange failed: 400 {\"error\":\"invalid_grant\"}") {
			t.Errorf("%+v", run)
		}
	})
	t.Run("no refresh token", func(t *testing.T) {
		setEnv(t, "ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		setEnv(t, "GOOGLE_OAUTH_CLIENT_SECRET", "secret")
			srv := httptestServer(t, http.StatusOK, `{"access_token":"a","expires_in":3599,"token_type":"Bearer"}`)
		setEnv(t, calendar.GoogleTokenEnv, srv)
		run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=x", freePort(t))
		if run.code != cli.ExitError || run.stderr != "error: Google returned no refresh token; revoke the app's access and retry\n" {
			t.Errorf("%+v", run)
		}
	})
}

func httptestServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
