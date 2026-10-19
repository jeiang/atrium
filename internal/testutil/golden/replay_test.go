package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, strings.TrimSpace(sprintf(format, args...)))
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

func replayRequest(t *testing.T, want Request, req *http.Request) (*recorder, bool) {
	t.Helper()
	rec := &recorder{TB: t}
	return rec, checkReplayed(rec, 0, want, req, nil)
}

func request(t *testing.T, contentType, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", "/p", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func multipartBody(t *testing.T, tags, file string) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary("custom-boundary-1234"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("tags", tags); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("torrents", "a.torrent")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(file))
	_ = w.Close()
	return w.FormDataContentType(), buf.String()
}

func TestReplayBodyEncodings(t *testing.T) {
	mpType, mpBody := multipartBody(t, "media", "payload")
	mpWant := raw(t, []map[string]any{
		{"name": "tags", "filename": nil, "content_type": nil, "text": "media"},
		{"name": "torrents", "filename": "a.torrent", "content_type": "application/octet-stream", "text": "payload"},
	})
	otherType, otherBody := multipartBody(t, "media", "changed")
	otherBoundaryType := strings.ReplaceAll(mpType, "custom-boundary-1234", "zzz")
	otherBoundaryBody := strings.ReplaceAll(mpBody, "custom-boundary-1234", "zzz")

	cases := []struct {
		name        string
		want        Request
		contentType string
		body        string
		fail        bool
	}{
		{"json match", Request{BodyEncoding: "json", Body: raw(t, map[string]int{"a": 1})}, "application/json", `{ "a": 1 }`, false},
		{"json mismatch", Request{BodyEncoding: "json", Body: raw(t, map[string]int{"a": 1})}, "application/json", `{"a": 2}`, true},
		{"form match", Request{BodyEncoding: "form", Body: raw(t, [][]string{{"a", "x y"}, {"b", ""}, {"a", "z"}})}, "application/x-www-form-urlencoded", "a=x+y&b=&a=z", false},
		{"form value", Request{BodyEncoding: "form", Body: raw(t, [][]string{{"a", "1"}})}, "application/x-www-form-urlencoded", "a=2", true},
		{"form order", Request{BodyEncoding: "form", Body: raw(t, [][]string{{"a", "1"}, {"b", "2"}})}, "application/x-www-form-urlencoded", "b=2&a=1", true},
		{"form extra", Request{BodyEncoding: "form", Body: raw(t, [][]string{{"a", "1"}})}, "application/x-www-form-urlencoded", "a=1&b=2", true},
		{"multipart match", Request{BodyEncoding: "multipart", Body: mpWant}, mpType, mpBody, false},
		{"multipart boundary-insensitive", Request{BodyEncoding: "multipart", Body: mpWant}, otherBoundaryType, otherBoundaryBody, false},
		{"multipart body", Request{BodyEncoding: "multipart", Body: mpWant}, otherType, otherBody, true},
		{"multipart not multipart", Request{BodyEncoding: "multipart", Body: mpWant}, "text/plain", "x", true},
		{"text match", Request{BodyEncoding: "text", Body: raw(t, "hello")}, "", "hello", false},
		{"text mismatch", Request{BodyEncoding: "text", Body: raw(t, "hello")}, "", "hellp", true},
		{"base64 match", Request{BodyEncoding: "base64", Body: raw(t, "AP8=")}, "", "\x00\xff", false},
		{"base64 mismatch", Request{BodyEncoding: "base64", Body: raw(t, "AP8=")}, "", "\x00\xfe", true},
		{"none empty", Request{BodyEncoding: "none"}, "", "", false},
		{"none with body", Request{BodyEncoding: "none"}, "", "x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, ok := replayRequest(t, withPath(c.want), request(t, c.contentType, c.body))
			if failed := len(rec.errors) > 0; failed != c.fail || ok == c.fail {
				t.Fatalf("errors = %q, ok = %v, want failure %v", rec.errors, ok, c.fail)
			}
		})
	}
}

func withPath(r Request) Request {
	r.Method, r.Path = "POST", "/p"
	return r
}

func TestReplayUnknownEncodingIsRejected(t *testing.T) {
	rec, ok := replayRequest(t, withPath(Request{BodyEncoding: "yaml"}), request(t, "", ""))
	if ok || len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "unknown body_encoding") {
		t.Fatalf("ok = %v, errors = %q", ok, rec.errors)
	}
}

func TestReplayServerCatchesBodyMismatch(t *testing.T) {
	want := Request{Method: "POST", Path: "/p", BodyEncoding: "text", Body: raw(t, "hello")}
	resp := raw(t, Response{Status: 200, BodyEncoding: "none"})
	srv := Replay(t, []Exchange{{Request: want, Response: resp}})
	res, err := http.Post(srv.URL+"/p", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestReplayServerAnswers500OnBodyMismatch(t *testing.T) {
	rec := &recorder{TB: t}
	want := Request{Method: "POST", Path: "/p", BodyEncoding: "text", Body: raw(t, "hello")}
	srv := Replay(rec, []Exchange{{Request: want, Response: raw(t, Response{Status: 200, BodyEncoding: "none"})}})
	res, err := http.Post(srv.URL+"/p", "text/plain", strings.NewReader("other"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError || len(rec.errors) != 1 {
		t.Fatalf("status %d, errors %q", res.StatusCode, rec.errors)
	}
}
