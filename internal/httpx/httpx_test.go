package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecodeReplaceMatchesPython(t *testing.T) {
	for in, want := range map[string]string{
		"ok":                 "ok",
		"a\xe2\x82":          "a\ufffd",
		"\xed\xa0\x80x":      "\ufffd\ufffd\ufffdx",
		"\xc0\xaf":           "\ufffd\ufffd",
		"\xf0\x9f\x98":       "\ufffd",
		"\xf4\x90\x80\x80":   "\ufffd\ufffd\ufffd\ufffd",
		"\xe0\x80\x80":       "\ufffd\ufffd\ufffd",
		"\xff\xfe":           "\ufffd\ufffd",
		"\xf0\x9f\x98\x80":   "😀",
		"\xe2\x82\xac\xe2":   "€\ufffd",
		"\xef\xbf\xbd\x80ok": "\ufffd\ufffdok",
	} {
		if got := DecodeReplace([]byte(in)); got != want {
			t.Errorf("DecodeReplace(%q) = %q, want %q", in, got, want)
		}
	}
}

func unavailable(t *testing.T, err error, prefix string) {
	t.Helper()
	var su *ServiceUnavailable
	if !errors.As(err, &su) || !strings.HasPrefix(su.Error(), prefix) {
		t.Errorf("err = %v, want ServiceUnavailable %q", err, prefix)
	}
}

func TestPostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("%s %s", r.Method, r.Header.Get("Content-Type"))
			}
			_, _ = w.Write([]byte(`{"a": 1}`))
		case "/empty":
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	got, err := PostJSON(t.Context(), srv.URL+"/ok", map[string]int{"x": 1})
	if err != nil || string(got) != `{"a": 1}` {
		t.Errorf("PostJSON = %s, %v", got, err)
	}
	if _, err := PostJSON(t.Context(), srv.URL+"/empty", nil); !errors.Is(err, ErrValue) {
		t.Errorf("empty reply: %v", err)
	}
	_, err = PostJSON(t.Context(), srv.URL+"/moved", nil)
	unavailable(t, err, srv.URL+"/moved: HTTP Error 307: Temporary Redirect")
	_, err = PostJSON(t.Context(), "ftp://x/embeddings", nil)
	unavailable(t, err, "no service URL configured for 'ftp://x/embeddings'")
}

func TestTimeoutsAndCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	_, err := GetText(t.Context(), srv.URL, 50*time.Millisecond)
	unavailable(t, err, srv.URL+": <urlopen error timed out>")
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = PostJSON(ctx, srv.URL, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestGetText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("%s %q", r.Method, r.Header.Get("User-Agent"))
		}
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("rate \xff"))
	}))
	defer srv.Close()
	got, err := GetText(t.Context(), srv.URL, 0)
	if err != nil || got != "rate \ufffd" {
		t.Errorf("GetText = %q, %v", got, err)
	}
	_, err = GetText(t.Context(), srv.URL+"/missing", 0)
	unavailable(t, err, srv.URL+"/missing: HTTP Error 404: Not Found")
}
