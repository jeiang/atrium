package calendar

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"
)

const (
	authURI        = "https://accounts.google.com/o/oauth2/v2/auth"
	AuthTimeout    = 300
	donePage       = "<!doctype html><title>atrium</title><p>Authorised. You can close this tab."
	exchangeWindow = 30 * time.Second
)

var tokenURLSafe = func(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func UseTokens(f func(n int) string) func() {
	saved := tokenURLSafe
	tokenURLSafe = f
	return func() { tokenURLSafe = saved }
}

func AuthURL(clientID, redirectURI, state, challenge string) string {
	pairs := [][2]string{
		{"client_id", clientID}, {"redirect_uri", redirectURI}, {"response_type", "code"}, {"scope", Scope},
		{"access_type", "offline"}, {"prompt", "consent"}, {"state", state}, {"code_challenge", challenge},
		{"code_challenge_method", "S256"},
	}
	return authURI + "?" + formBody(pairs)
}

func unquotePlus(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

func queryParams(query string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(query, "&") {
		name, value, found := strings.Cut(pair, "=")
		if !found || value == "" {
			continue
		}
		out[unquotePlus(name)] = unquotePlus(value)
	}
	return out
}

type AuthOptions struct {
	ClientID     string
	ClientSecret string
	Port         int
	Out          io.Writer
	Timeout      int
}

func Authorize(ctx context.Context, o AuthOptions) (string, error) {
	if o.ClientID == "" || o.ClientSecret == "" {
		return "", Error("ATRIUM_CALENDAR_GOOGLE_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required")
	}
	if o.Timeout == 0 {
		o.Timeout = AuthTimeout
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
	if err != nil {
		return "", remote(err)
	}
	result := make(chan map[string]string, 1)
	served := make(chan struct{}, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, fmt.Sprintf("Unsupported method ('%s')", r.Method), http.StatusNotImplemented)
			return
		}
		params := queryParams(r.URL.RawQuery)
		_, hasCode := params["code"]
		_, hasError := params["error"]
		if hasCode || hasError {
			select {
			case result <- params:
			default:
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, donePage)
		select {
		case served <- struct{}{}:
		default:
		}
	})}
	go func() { _ = srv.Serve(listener) }()
	redirect := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	verifier := tokenURLSafe(64)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	state := tokenURLSafe(16)
	_, _ = fmt.Fprintf(o.Out, "Open this URL in a browser and approve access:\n\n%s\n\n", AuthURL(o.ClientID, redirect, state, challenge))
	got, err := waitForRedirect(ctx, result, served, o.Timeout)
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	_ = srv.Shutdown(closeCtx)
	cancel()
	if err != nil {
		return "", err
	}
	if got == nil {
		return "", Error("timed out waiting for the Google redirect")
	}
	if msg := got["error"]; msg != "" {
		return "", Error("Google returned an error: " + msg)
	}
	if got["state"] != state {
		return "", Error("state mismatch; start again")
	}
	conf := &oauth2.Config{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{AuthURL: authURI, TokenURL: endpoints.Token, AuthStyle: oauth2.AuthStyleInParams},
	}
	exchange := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: exchangeWindow})
	token, err := conf.Exchange(exchange, got["code"], oauth2.VerifierOption(verifier))
	var failed *oauth2.RetrieveError
	switch {
	case errors.As(err, &failed):
		body := string(failed.Body)
		if utf8.RuneCountInString(body) > 200 {
			body = string([]rune(body)[:200])
		}
		return "", Error(fmt.Sprintf("token exchange failed: %d %s", failed.Response.StatusCode, body))
	case err != nil && strings.Contains(err.Error(), "missing access_token"):
		return "", Error("Google returned no refresh token; revoke the app's access and retry")
	case err != nil:
		return "", remote(err)
	case token.RefreshToken == "":
		return "", Error("Google returned no refresh token; revoke the app's access and retry")
	}
	return token.RefreshToken, nil
}

func waitForRedirect(ctx context.Context, result chan map[string]string, served chan struct{}, limit int) (map[string]string, error) {
	for waited := 0; waited < limit; waited++ {
		select {
		case got := <-result:
			return got, nil
		default:
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-served:
			timer.Stop()
		case <-timer.C:
		}
	}
	select {
	case got := <-result:
		return got, nil
	default:
		return nil, nil
	}
}
