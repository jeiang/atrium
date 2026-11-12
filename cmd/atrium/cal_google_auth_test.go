//go:build !modernc

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
)

type calTokenFake struct {
	mu    sync.Mutex
	forms []url.Values
}

func (f *calTokenFake) handler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 3599})
}

func (f *calTokenFake) seen() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.forms...)
}

func calHostConfig(t *testing.T, dir string) {
	t.Helper()
	secret := filepath.Join(dir, "client_secret")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `
[connections."google/default"]
kind = "google"
label = "Google"
config = { client_id = "client-id" }
capabilities = ["calendar"]
credential_files = { "oauth/client_secret" = "` + secret + `" }
`
	path := filepath.Join(dir, "host.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", path)
}

func calRunGoogleAuth(t *testing.T, redirect func(consent url.Values) url.Values) (appRun, url.Values, string) {
	t.Helper()
	requireCipher(t)
	dir := t.TempDir()
	setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	setEnv(t, "ATRIUM_CONFIG", "")
	calHostConfig(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errs lockedBuffer
	done := make(chan int, 1)
	app := newTestApp(t)
	go func() { done <- app.Run(ctx, []string{"calendar", "google-auth", "--port", "0"}, &out, &errs) }()
	var consent url.Values
	if redirect != nil {
		for consent == nil {
			for line := range strings.SplitSeq(out.String(), "\n") {
				if u, err := url.Parse(line); err == nil && strings.HasPrefix(line, "https://") {
					consent = u.Query()
				}
			}
			if consent != nil {
				break
			}
			select {
			case code := <-done:
				t.Fatalf("google-auth exited %d before printing the consent URL: %q %q", code, out.String(), errs.String())
			case <-time.After(20 * time.Millisecond):
			}
		}
		resp, err := http.Get(consent.Get("redirect_uri") + "/?" + redirect(consent).Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	select {
	case code := <-done:
		return appRun{code, out.String(), errs.String()}, consent, dir
	case <-ctx.Done():
		t.Fatal("calendar google-auth did not finish")
		return appRun{}, nil, dir
	}
}

func TestCalendarGoogleAuthExchangesTheCodeWithPKCEAndRefusesAForeignState(t *testing.T) {
	t.Run("client not set", func(t *testing.T) {
		dir := t.TempDir()
		setEnv(t, "ATRIUM_DB", filepath.Join(dir, "atrium.db"))
		setEnv(t, "ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
		setEnv(t, "ATRIUM_CONFIG", "")
		var out, errs lockedBuffer
		app := newTestApp(t)
		code := app.Run(context.Background(), []string{"calendar", "google-auth", "--port", "0"}, &out, &errs)
		if code != cli.ExitError || out.String() != "" || errs.String() != "error: the google/default connection is not configured\n" {
			t.Fatalf("%d %q %q", code, out.String(), errs.String())
		}
	})

	fake := &calTokenFake{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	t.Run("state mismatch", func(t *testing.T) {
		setEnv(t, calendar.GoogleTokenEnv, srv.URL+"/token")
		run, _, _ := calRunGoogleAuth(t, func(url.Values) url.Values { return url.Values{"state": {"forged"}, "code": {"x"}} })
		if run.code != cli.ExitError || strings.Contains(run.stdout, "refresh-1") || run.stderr != "error: state mismatch; start again\n" {
			t.Fatalf("%+v", run)
		}
		if n := len(fake.seen()); n != 0 {
			t.Fatalf("a forged redirect reached the token endpoint %d times", n)
		}
	})

	t.Run("code exchange", func(t *testing.T) {
		setEnv(t, calendar.GoogleTokenEnv, srv.URL+"/token")
		run, consent, dir := calRunGoogleAuth(t, func(c url.Values) url.Values {
			return url.Values{"state": {c.Get("state")}, "code": {"google-code"}, "scope": {"calendar"}}
		})
		if run.code != cli.ExitOK || !strings.Contains(run.stdout, "Google Calendar consent stored") || strings.Contains(run.stdout, "refresh-1") || run.stderr != "" {
			t.Fatalf("%+v", run)
		}
		if consent.Get("client_id") != "client-id" || consent.Get("access_type") != "offline" || consent.Get("prompt") != "consent" ||
			consent.Get("code_challenge_method") != "S256" || !strings.Contains(consent.Get("scope"), "calendar.events") {
			t.Fatalf("consent URL query %v", consent)
		}
		forms := fake.seen()
		if len(forms) != 1 {
			t.Fatalf("%d token requests, want 1", len(forms))
		}
		form := forms[0]
		digest := sha256.Sum256([]byte(form.Get("code_verifier")))
		if form.Get("grant_type") != "authorization_code" || form.Get("code") != "google-code" || form.Get("redirect_uri") != consent.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(digest[:]) != consent.Get("code_challenge") {
			t.Fatalf("token request %v does not match the consent URL %v", form, consent)
		}
		d := keyedDB(t, filepath.Join(dir, "atrium.db"))
		if got := importValue(t, d.Read, "SELECT value FROM connection_credentials WHERE connection_id = 'google/default' AND slot = 'oauth/calendar/refresh'"); got != "refresh-1" {
			t.Fatalf("stored calendar grant = %q", got)
		}
	})
}
