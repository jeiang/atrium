package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashedAssetsNegotiateAndCache(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	base := startServeIn(t, dir)
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "web", "static", "assets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var assets map[string]string
	if err := json.Unmarshal(raw, &assets); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	request := func(method, url, accept, etag string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", accept)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}
	for logical, url := range assets {
		if !strings.HasSuffix(logical, ".css") && !strings.HasSuffix(logical, ".js") {
			continue
		}
		t.Run(logical, func(t *testing.T) {
			identity, plain := request(http.MethodGet, url, "br;q=0, gzip;q=0", "")
			if identity.StatusCode != http.StatusOK || identity.Header.Get("Content-Encoding") != "" {
				t.Fatalf("identity: %d %v", identity.StatusCode, identity.Header)
			}
			for _, accept := range []string{"br", "gzip;q=1, br;q=0", "gzip;q=0.5, br;q=1", "*;q=1, br;q=0"} {
				encoding := "br"
				if strings.Contains(accept, "br;q=0") {
					encoding = "gzip"
				}
				resp, body := request(http.MethodGet, url, accept, "")
				if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != encoding || resp.Header.Get("Vary") != "Accept-Encoding" || resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
					t.Fatalf("%s: %d %v", accept, resp.StatusCode, resp.Header)
				}
				digest := sha256.Sum256(body)
				etag := `"` + hex.EncodeToString(digest[:]) + `"`
				if resp.Header.Get("ETag") != etag || etag == identity.Header.Get("ETag") {
					t.Fatalf("representation ETag: %v", resp.Header)
				}
				staged, err := os.ReadFile(filepath.Join("..", "..", "internal", "web", "static", strings.TrimPrefix(url, "/static/")) + map[string]string{"br": ".br", "gzip": ".gz"}[encoding])
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(body, staged) {
					t.Fatal("encoded response differs from prepared asset")
				}
				if encoding == "gzip" {
					reader, err := gzip.NewReader(bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					decoded, err := io.ReadAll(reader)
					_ = reader.Close()
					if err != nil || !bytes.Equal(decoded, plain) {
						t.Fatalf("gzip identity: %v", err)
					}
				}
				cached, body := request(http.MethodGet, url, accept, etag)
				if cached.StatusCode != http.StatusNotModified || len(body) != 0 || cached.Header.Get("ETag") != etag {
					t.Fatalf("conditional response: %d %v", cached.StatusCode, cached.Header)
				}
				head, body := request(http.MethodHead, url, accept, "")
				if head.StatusCode != http.StatusOK || len(body) != 0 || head.Header.Get("ETag") != etag || head.Header.Get("Content-Length") != resp.Header.Get("Content-Length") || head.Header.Get("Content-Encoding") != encoding {
					t.Fatalf("HEAD: %d %v", head.StatusCode, head.Header)
				}
			}
			refused, _ := request(http.MethodGet, url, "identity;q=0, *;q=0", "")
			if refused.StatusCode != http.StatusNotAcceptable {
				t.Fatalf("all encodings refused: %d", refused.StatusCode)
			}
		})
	}
	for _, url := range []string{"/static/0000000000000000/app.css", "/static/app.css"} {
		resp, _ := request(http.MethodGet, url, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown asset %s: %d", url, resp.StatusCode)
		}
	}
	// HTML pages are compressed by the gzip middleware when the client accepts it.
	page, compressed := request(http.MethodGet, "/about", "gzip", "")
	if page.StatusCode != http.StatusOK || page.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(page.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("HTML page: %d %v", page.StatusCode, page.Header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	if html, err := io.ReadAll(zr); err != nil || !strings.Contains(string(html), "</html>") {
		t.Fatalf("HTML page body: %v %.200s", err, html)
	}
	worker, script := request(http.MethodGet, "/static/sw.js", "", "")
	if worker.StatusCode != http.StatusOK || worker.Header.Get("Cache-Control") != "no-cache" || worker.Header.Get("Service-Worker-Allowed") != "/" || !strings.Contains(string(script), assets["app.css"]) {
		t.Errorf("service worker: %d %v", worker.StatusCode, worker.Header)
	}
}
