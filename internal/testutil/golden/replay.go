package golden

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Response is the recorded reply of an Exchange.
type Response struct {
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	BodyEncoding string            `json:"body_encoding"`
	Body         json.RawMessage   `json:"body"`
}

// Replay serves exchanges in order: each request must match the recorded
// method, path, query, recorded headers and body (json canonically, form as
// ordered pairs, multipart as decoded parts, text and base64 as bytes, none as
// empty; any other encoding is an error), and gets the recorded response. Requests beyond the recording
// get 599; t fails at cleanup unless every exchange was served.
func Replay(t testing.TB, exchanges []Exchange) *httptest.Server {
	t.Helper()
	return ReplayWith(t, exchanges, nil)
}

// ReplayWith is Replay for a client that spells a query differently from the recorded one: query, when not nil,
// normalizes the recorded and the received raw query before they are compared.
func ReplayWith(t testing.TB, exchanges []Exchange, query func(string) string) *httptest.Server {
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
		if !checkReplayed(t, i, exchanges[i].Request, r, query) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
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

func checkReplayed(t testing.TB, i int, want Request, r *http.Request, query func(string) string) bool {
	switch want.BodyEncoding {
	case "json", "form", "multipart", "text", "base64", "none":
	default:
		t.Errorf("golden replay: request %d: unknown body_encoding %q", i, want.BodyEncoding)
		return false
	}
	gotQuery, wantQuery := r.URL.RawQuery, want.Query
	if query != nil {
		gotQuery, wantQuery = query(gotQuery), query(wantQuery)
	}
	if r.Method != want.Method || r.URL.EscapedPath() != want.Path || gotQuery != wantQuery {
		t.Errorf("golden replay: request %d: %s %s?%s, want %s %s?%s", i, r.Method, r.URL.EscapedPath(), gotQuery, want.Method, want.Path, wantQuery)
	}
	for k, v := range want.Headers {
		if got := r.Header.Get(k); got != v && (!strings.HasPrefix(v, "multipart/") || !strings.HasPrefix(got, v)) {
			t.Errorf("golden replay: request %d: header %s = %q, want %q", i, k, got, v)
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("golden replay: request %d: %v", i, err)
		return false
	}
	if msg := compareBody(want, r.Header.Get("Content-Type"), body); msg != "" {
		t.Errorf("golden replay: request %d body differs from the golden:\n%s", i, msg)
		return false
	}
	return true
}

type multipartPart struct {
	Name        string  `json:"name"`
	Filename    *string `json:"filename"`
	ContentType *string `json:"content_type"`
	Text        *string `json:"text,omitempty"`
	Base64      *string `json:"base64,omitempty"`
}

func compareBody(want Request, contentType string, body []byte) string {
	switch want.BodyEncoding {
	case "json":
		diffs, err := Diff(want.Body, body)
		if err != nil {
			return err.Error()
		}
		return strings.Join(diffs, "\n")
	case "form":
		var pairs [][]string
		if err := json.Unmarshal(want.Body, &pairs); err != nil {
			return "recorded form body: " + err.Error()
		}
		got, err := formPairs(string(body))
		if err != nil {
			return err.Error()
		}
		return diffValues(pairs, got)
	case "multipart":
		var parts []multipartPart
		if err := json.Unmarshal(want.Body, &parts); err != nil {
			return "recorded multipart body: " + err.Error()
		}
		got, err := multipartParts(contentType, body)
		if err != nil {
			return err.Error()
		}
		return diffValues(parts, got)
	case "text":
		var s string
		if err := json.Unmarshal(want.Body, &s); err != nil {
			return "recorded text body: " + err.Error()
		}
		return diffBytes([]byte(s), body)
	case "base64":
		var s string
		if err := json.Unmarshal(want.Body, &s); err != nil {
			return "recorded base64 body: " + err.Error()
		}
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return "recorded base64 body: " + err.Error()
		}
		return diffBytes(raw, body)
	case "none":
		if len(body) != 0 {
			return fmt.Sprintf("want an empty body, got %d bytes: %q", len(body), body)
		}
		return ""
	}
	return "unknown body_encoding " + want.BodyEncoding
}

func diffBytes(want, got []byte) string {
	if bytes.Equal(want, got) {
		return ""
	}
	return fmt.Sprintf("want %q\ngot  %q", want, got)
}

func diffValues(want, got any) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	return fmt.Sprintf("want %s\ngot  %s", w, g)
}

func formPairs(raw string) ([][]string, error) {
	pairs := [][]string{}
	for _, seg := range strings.Split(raw, "&") {
		if seg == "" {
			continue
		}
		name, value, _ := strings.Cut(seg, "=")
		n, err := url.QueryUnescape(name)
		if err != nil {
			return nil, err
		}
		v, err := url.QueryUnescape(value)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, []string{n, v})
	}
	return pairs, nil
}

func multipartParts(contentType string, body []byte) ([]multipartPart, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, fmt.Errorf("content-type %q is not multipart/form-data with a boundary", contentType)
	}
	rd := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	parts := []multipartPart{}
	for {
		p, err := rd.NextRawPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(p)
		if err != nil {
			return nil, err
		}
		entry := multipartPart{Name: p.FormName()}
		if f := p.FileName(); f != "" {
			entry.Filename = &f
		}
		if ct := p.Header.Get("Content-Type"); ct != "" {
			mt, _, err := mime.ParseMediaType(ct)
			if err != nil {
				mt = ct
			}
			entry.ContentType = &mt
		}
		if utf8.Valid(data) {
			s := string(data)
			entry.Text = &s
		} else {
			b := base64.StdEncoding.EncodeToString(data)
			entry.Base64 = &b
		}
		parts = append(parts, entry)
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
