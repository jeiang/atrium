package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const consentURL = "https://accounts.google.com/o/oauth2/v2/auth?client_id=golden-google-client-id&redirect_uri=http%3A%2F%2F127.0.0.1%3A8765" +
	"&response_type=code&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcalendar.events&access_type=offline&prompt=consent" +
	"&state=golden-token-urlsafe-16&code_challenge=0rnfdO8zLHbPYynUP5e9BxvYBwHL12yVVJTRDlfzAis&code_challenge_method=S256"

func noTools() (*registry.Registry, error) { return registry.New(), nil }

func pinnedTokens(n int) string { return fmt.Sprintf("golden-token-urlsafe-%d", n) }

func googleAuthApp() *cli.App {
	app := cli.New("atrium", logx.Setup)
	app.Add(pinned([]*cli.Command{calendarcmd.Command(noTools)})...)
	return app
}

type authRun struct {
	code           int
	stdout, stderr string
}

func runGoogleAuth(t *testing.T, redirect string, args ...string) authRun {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CONFIG", "")
	done := make(chan authRun, 1)
	go func() {
		var out, errs bytes.Buffer
		code := googleAuthApp().Run(context.Background(), append([]string{"calendar", "google-auth"}, args...), &out, &errs)
		done <- authRun{code, out.String(), errs.String()}
	}()
	if redirect != "" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := http.Get("http://127.0.0.1:8765/" + redirect)
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
	t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "golden-google-client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "golden-google-client-secret")
	t.Cleanup(calendar.UseTokens(pinnedTokens))
	srv := golden.Replay(t, golden.LoadScenario(t, "google_auth", "code_exchange").Exchanges)
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{Token: srv.URL + "/token"}))
	run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=golden-google-code&scope=calendar", "--port", "8765")
	want := "Open this URL in a browser and approve access:\n\n" + consentURL + "\n\n\nGOOGLE_REFRESH_TOKEN=golden-google-refresh-token\n"
	if run.code != cli.ExitOK || run.stdout != want || run.stderr != "" {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %q", run.code, run.stdout, want, run.stderr)
	}
}

func TestGoogleAuthRefusals(t *testing.T) {
	t.Run("client not set", func(t *testing.T) {
		t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
		run := runGoogleAuth(t, "")
		if run.code != cli.ExitError || run.stdout != "" || run.stderr != "error: ATRIUM_CALENDAR_GOOGLE_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("provider error", func(t *testing.T) {
		t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
		t.Cleanup(calendar.UseTokens(pinnedTokens))
		run := runGoogleAuth(t, "?error=access_denied", "--port", "8765")
		if run.code != cli.ExitError || run.stderr != "error: Google returned an error: access_denied\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
		t.Cleanup(calendar.UseTokens(pinnedTokens))
		run := runGoogleAuth(t, "?state=other&code=x", "--port", "8765")
		if run.code != cli.ExitError || run.stderr != "error: state mismatch; start again\n" {
			t.Errorf("%+v", run)
		}
	})
	t.Run("token exchange failed", func(t *testing.T) {
		t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
		t.Cleanup(calendar.UseTokens(pinnedTokens))
		srv := httptestServer(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
		t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{Token: srv}))
		run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=x", "--port", "8765")
		if run.code != cli.ExitError || !strings.HasPrefix(run.stderr, "error: token exchange failed: 400 {\"error\":\"invalid_grant\"}") {
			t.Errorf("%+v", run)
		}
	})
	t.Run("no refresh token", func(t *testing.T) {
		t.Setenv("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID", "id")
		t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
		t.Cleanup(calendar.UseTokens(pinnedTokens))
		srv := httptestServer(t, http.StatusOK, `{"access_token":"a","expires_in":3599,"token_type":"Bearer"}`)
		t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{Token: srv}))
		run := runGoogleAuth(t, "?state=golden-token-urlsafe-16&code=x", "--port", "8765")
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
