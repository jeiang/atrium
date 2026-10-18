package golden

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Response is the recorded reply of an Exchange.
type Response struct {
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	BodyEncoding string            `json:"body_encoding"`
	Body         json.RawMessage   `json:"body"`
}

// Replay serves exchanges in order: each request must match the recorded
// method, path, query, recorded headers and (for json bodies) canonical
// JSON body, and gets the recorded response. Requests beyond the recording
// get 599; t fails at cleanup unless every exchange was served.
func Replay(t testing.TB, exchanges []Exchange) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := next
		next++
		mu.Unlock()
		if i >= len(exchanges) {
			t.Errorf("golden replay: unexpected request %d: %s %s", i, r.Method, r.URL)
			w.WriteHeader(599)
			return
		}
		checkReplayed(t, i, exchanges[i].Request, r)
		var resp Response
		if err := json.Unmarshal(exchanges[i].Response, &resp); err != nil {
			t.Errorf("golden replay: response %d: %v", i, err)
			w.WriteHeader(599)
			return
		}
		body, err := responseBody(resp)
		if err != nil {
			t.Errorf("golden replay: response %d: %v", i, err)
			w.WriteHeader(599)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(func() {
		srv.Close()
		mu.Lock()
		defer mu.Unlock()
		if next < len(exchanges) {
			t.Errorf("golden replay: %d of %d exchanges served", next, len(exchanges))
		}
	})
	return srv
}

func checkReplayed(t testing.TB, i int, want Request, r *http.Request) {
	if r.Method != want.Method || r.URL.Path != want.Path || r.URL.RawQuery != want.Query {
		t.Errorf("golden replay: request %d: %s %s?%s, want %s %s?%s", i, r.Method, r.URL.Path, r.URL.RawQuery, want.Method, want.Path, want.Query)
	}
	for k, v := range want.Headers {
		if got := r.Header.Get(k); got != v && (!strings.HasPrefix(v, "multipart/") || !strings.HasPrefix(got, v)) {
			t.Errorf("golden replay: request %d: header %s = %q, want %q", i, k, got, v)
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("golden replay: request %d: %v", i, err)
		return
	}
	if want.BodyEncoding != "json" {
		return
	}
	diffs, err := Diff(want.Body, body)
	if err != nil {
		t.Errorf("golden replay: request %d body: %v", i, err)
		return
	}
	if len(diffs) > 0 {
		t.Errorf("golden replay: request %d body differs from the golden:\n%s", i, strings.Join(diffs, "\n"))
	}
}

func responseBody(resp Response) ([]byte, error) {
	switch resp.BodyEncoding {
	case "json":
		return resp.Body, nil
	case "text":
		var s string
		err := json.Unmarshal(resp.Body, &s)
		return []byte(s), err
	case "base64":
		var s string
		if err := json.Unmarshal(resp.Body, &s); err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(s)
	}
	return nil, nil
}
