package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGzipSetsVaryOnce(t *testing.T) {
	body := bytes.Repeat([]byte("a"), gzipMinSize+1)
	handler := Gzip(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	for _, accept := range []string{"gzip", ""} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
			t.Errorf("accept %q: Vary %v", accept, got)
		}
	}
}
