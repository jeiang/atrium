package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web"
)

func projectPath(id int64, rest string) string {
	return "/api/v1/projects/" + strconv.FormatInt(id, 10) + rest
}

func uploadBody(t *testing.T, fields map[string]string, name, data string) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if name != "" {
		part, err := writer.CreateFormFile("upload", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), writer.FormDataContentType()
}

func rawFileRequest(t *testing.T, s *apiSite, token, method, path, contentType string, body io.Reader, headers ...string) apiReply {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := apiReply{status: resp.StatusCode, header: resp.Header, body: string(data)}
	_ = json.Unmarshal(data, &out.json)
	return out
}

func TestDeclaredMultipartAndBinaryAPI(t *testing.T) {
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	temp := filepath.Join(dir, "temp")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	crash := filepath.Join(temp, "command-body-crashed")
	if err := os.WriteFile(crash, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := localAPIToken(t, "files")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	if _, err := os.Stat(crash); !os.IsNotExist(err) {
		t.Fatalf("crash spool remains: %v", err)
	}
	seed, err := http.Get(base + "/projects/new")
	if err != nil {
		t.Fatal(err)
	}
	_ = seed.Body.Close()
	var csrf *http.Cookie
	for _, cookie := range seed.Cookies() {
		if cookie.Name == web.CSRFCookie {
			csrf = cookie
		}
	}
	if csrf == nil {
		t.Fatal("no CSRF cookie")
	}
	form := url.Values{"name": {"Transport"}, "private": {"1"}, "effort": {"high"}, "context_limit": {"200000"}}
	req, err := http.NewRequest(http.MethodPost, base+"/projects", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.AddCookie(csrf)
	req.Header.Set(web.CSRFHeader, csrf.Value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("create HTML project: %d %s", resp.StatusCode, createdBody)
	}
	var project int64
	if err := d.Read.QueryRow("SELECT id FROM projects WHERE name = 'Transport'").Scan(&project); err != nil {
		t.Fatal(err)
	}
	body, media := uploadBody(t, nil, "unsafe.html", "<script>untrusted</script>")
	first := s.do(http.MethodPost, projectPath(project, "/files"), token, body, "Content-Type", media, "Idempotency-Key", "upload-transport")
	if first.status != http.StatusCreated {
		t.Fatalf("upload: %d %s", first.status, first.body)
	}
	ids := first.json["file_ids"].([]any)
	file := number(t, ids[0])
	replay := s.do(http.MethodPost, projectPath(project, "/files"), token, body, "Content-Type", media, "Idempotency-Key", "upload-transport")
	if replay.status != first.status || replay.body != first.body || replay.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("multipart replay: %d %s", replay.status, replay.body)
	}
	changed, changedType := uploadBody(t, nil, "unsafe.html", "different")
	expectAPI(t, s.do(http.MethodPost, projectPath(project, "/files"), token, changed, "Content-Type", changedType, "Idempotency-Key", "upload-transport"), 422, "idempotency_mismatch")
	path := projectPath(project, "/files/"+strconv.FormatInt(file, 10)+"/content")
	got := s.do(http.MethodGet, path, token, nil, "Range", "bytes=0-6")
	if got.status != 206 || got.body != "<script" || got.header.Get("Content-Range") != "bytes 0-6/26" || got.header.Get("Accept-Ranges") != "bytes" || got.header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(got.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("binary range: %d %q %v", got.status, got.body, got.header)
	}
	if got := s.do(http.MethodGet, path, token, nil, "Range", "bytes=999-1000"); got.status != 416 {
		t.Fatalf("range beyond file: %d", got.status)
	}
	expectAPI(t, s.do(http.MethodPost, "/api/v1/threads", token, body, "Content-Type", media), 415, "unsupported_media_type")
	expectAPI(t, s.do(http.MethodPost, projectPath(project, "/files"), token, map[string]any{}), 415, "unsupported_media_type")
	var readers []io.Reader
	chunk := strings.Repeat("x", 1<<20)
	for range 66 {
		readers = append(readers, strings.NewReader(chunk))
	}
	large := rawFileRequest(t, s, token, http.MethodPost, projectPath(project, "/files"), "multipart/form-data; boundary=large", io.MultiReader(readers...))
	expectAPI(t, large, 413, "too_large")
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("request spools remain: %v %v", entries, err)
	}
}
