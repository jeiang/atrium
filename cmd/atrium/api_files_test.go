package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"

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
	expectAPI(t, s.do(http.MethodPost, "/api/v1/today", token, body, "Content-Type", media), 415, "unsupported_media_type")
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

func apiItems(t *testing.T, reply apiReply) []any {
	t.Helper()
	if reply.status != http.StatusOK {
		t.Fatalf("list: %d %s", reply.status, reply.body)
	}
	items, ok := reply.json["items"].([]any)
	if !ok {
		t.Fatalf("list has no items: %s", reply.body)
	}
	return items
}

func TestFilesAndProjectsAPI(t *testing.T) {
	dir, model, f := moduleFixture(t, flagsExcept("", ""))
	warm(t, model)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	token := localAPIToken(t, "project-api")
	base, _, stop, err := startServeAppArgs(t, f.app, &lockedBuffer{}, "--auth", "local")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	s := &apiSite{t: t, base: base}
	create := func(name string, private bool) int64 {
		t.Helper()
		reply := s.do(http.MethodPost, "/api/v1/projects", token, map[string]any{"name": name, "instructions": "Keep the brief.", "effort": "high", "context_limit": 200000, "private": private})
		if reply.status != 201 {
			t.Fatalf("create project: %d %s", reply.status, reply.body)
		}
		return number(t, reply.json["id"])
	}
	project := create("Private project", true)
	other := create("Other project", false)
	detail := s.do(http.MethodGet, projectPath(project, ""), token, nil)
	if detail.status != 200 {
		t.Fatalf("detail: %d %s", detail.status, detail.body)
	}
	before := detail.header.Get("ETag")
	if got := s.do(http.MethodGet, projectPath(project, ""), token, nil, "If-None-Match", before); before == "" || got.status != 304 {
		t.Fatalf("project conditional: %q %d", before, got.status)
	}
	patch := s.do(http.MethodPatch, projectPath(project, ""), token, map[string]any{"name": "Renamed"})
	if patch.status != 200 {
		t.Fatalf("patch: %d %s", patch.status, patch.body)
	}
	detail = s.do(http.MethodGet, projectPath(project, ""), token, nil, "If-None-Match", before)
	row := detail.json["project"].(map[string]any)
	if detail.status != 200 || detail.header.Get("ETag") == before || row["name"] != "Renamed" || row["instructions"] != "Keep the brief." || row["effort"] != "high" || row["context_limit"] != float64(200000) || row["private"] != true {
		t.Fatalf("patch lost fields: %d %s", detail.status, detail.body)
	}
	invalid := s.do(http.MethodPatch, projectPath(project, ""), token, map[string]any{"name": " "})
	expectAPI(t, invalid, 422, "invalid")
	if fields := invalid.json["error"].(map[string]any)["fields"]; fields == nil {
		t.Fatal("missing validation fields")
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/projects/999999", token, nil), 404, "not_found")
	empty := s.do(http.MethodGet, projectPath(project, "/files"), token, nil)
	if len(apiItems(t, empty)) != 0 {
		t.Fatalf("new project has files: %s", empty.body)
	}
	fileETag := empty.header.Get("ETag")
	if conditional := s.do(http.MethodGet, projectPath(project, "/files"), token, nil, "If-None-Match", fileETag); fileETag == "" || conditional.status != 304 {
		t.Fatalf("files conditional: %q %d", fileETag, conditional.status)
	}
	body, media := uploadBody(t, nil, "note.txt", "first bytes of the note")
	uploaded := s.do(http.MethodPost, projectPath(project, "/files"), token, body, "Content-Type", media)
	if uploaded.status != 201 {
		t.Fatalf("upload: %d %s", uploaded.status, uploaded.body)
	}
	file := number(t, uploaded.json["file_ids"].([]any)[0])
	fileURL := projectPath(project, "/files/"+strconv.FormatInt(file, 10))
	listed := s.do(http.MethodGet, projectPath(project, "/files"), token, nil, "If-None-Match", fileETag)
	if len(apiItems(t, listed)) != 1 || listed.header.Get("ETag") == fileETag {
		t.Fatalf("file list did not change: %s", listed.body)
	}
	preview := s.do(http.MethodGet, fileURL, token, nil)
	if preview.status != 200 || preview.json["body"] != "first bytes of the note" {
		t.Fatalf("preview: %d %s", preview.status, preview.body)
	}
	if again := s.do(http.MethodGet, fileURL, token, nil, "If-None-Match", preview.header.Get("ETag")); preview.header.Get("ETag") == "" || again.status != 304 {
		t.Fatalf("preview conditional: %d", again.status)
	}
	full := s.do(http.MethodGet, fileURL+"/content", token, nil)
	if full.status != 200 || full.body != "first bytes of the note" || full.header.Get("Content-Type") != "text/plain" || full.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %s %v", full.status, full.body, full.header)
	}
	if conditional := s.do(http.MethodGet, fileURL+"/content", token, nil, "If-None-Match", full.header.Get("ETag")); full.header.Get("ETag") == "" || conditional.status != 304 {
		t.Fatalf("binary conditional: %d", conditional.status)
	}
	head := s.do(http.MethodHead, fileURL+"/content", token, nil)
	if head.status != 200 || head.body != "" || head.header.Get("Content-Length") != strconv.Itoa(len(full.body)) || head.header.Get("Content-Type") != full.header.Get("Content-Type") || head.header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD: %d %q %v", head.status, head.body, head.header)
	}
	ranged := s.do(http.MethodGet, fileURL+"/content", token, nil, "Range", "bytes=6-10")
	if ranged.status != 206 || ranged.body != "bytes" {
		t.Fatalf("range: %d %q", ranged.status, ranged.body)
	}
	if got := s.do(http.MethodGet, fileURL+"/content", token, nil, "Range", "bytes=999-"); got.status != 416 {
		t.Fatalf("bad range: %d", got.status)
	}
	if len(apiItems(t, s.do(http.MethodGet, projectPath(other, "/files"), token, nil))) != 0 {
		t.Fatal("private project's file leaked to other project list")
	}
	for _, suffix := range []string{"", "/content"} {
		expectAPI(t, s.do(http.MethodGet, projectPath(other, "/files/"+strconv.FormatInt(file, 10)+suffix), token, nil), 404, "not_found")
	}
	expectAPI(t, s.do(http.MethodDelete, projectPath(other, "/files/"+strconv.FormatInt(file, 10)), token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/projects/0/files", token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodGet, projectPath(project, "/files/not-an-id/content"), token, nil), 404, "not_found")
	expectAPI(t, s.do(http.MethodGet, "/api/v1/files/"+strconv.FormatInt(file, 10), token, nil), 404, "not_found")

	var bulk bytes.Buffer
	writer := multipart.NewWriter(&bulk)
	for i := range 105 {
		part, err := writer.CreateFormFile("upload", fmt.Sprintf("bulk-%03d.txt", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, "bulk note"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.do(http.MethodPost, projectPath(project, "/files"), token, bulk.String(), "Content-Type", writer.FormDataContentType()); got.status != 201 {
		t.Fatalf("bulk files: %d %s", got.status, got.body)
	}
	first := s.do(http.MethodGet, projectPath(project, "/files?limit=100"), token, nil)
	if len(apiItems(t, first)) != 100 {
		t.Fatalf("file first page: %s", first.body)
	}
	cursor := first.json["next_cursor"].(string)
	next := s.do(http.MethodGet, projectPath(project, "/files?limit=100&cursor="+url.QueryEscape(cursor)), token, nil)
	if len(apiItems(t, next)) != 6 || next.json["next_cursor"] != nil {
		t.Fatalf("file next page: %s", next.body)
	}
	expectAPI(t, s.do(http.MethodGet, projectPath(other, "/files?cursor="+url.QueryEscape(cursor)), token, nil), 400, "bad_cursor")
	expectAPI(t, s.do(http.MethodGet, projectPath(project, "/files?limit=101"), token, nil), 422, "invalid")
	seen := map[int64]bool{}
	for _, page := range []apiReply{first, next} {
		for _, raw := range apiItems(t, page) {
			id := number(t, raw.(map[string]any)["id"])
			if seen[id] {
				t.Fatalf("repeated file %d", id)
			}
			seen[id] = true
		}
	}

	token = localAPIToken(t, "thread-files")
	body, media = uploadBody(t, map[string]string{"web_search": "false"}, "thread.txt", "thread A only")
	started := s.do(http.MethodPost, "/api/v1/threads", token, body, "Content-Type", media)
	if started.status != 201 {
		t.Fatalf("multipart start: %d %s", started.status, started.body)
	}
	thread := number(t, started.json["thread"].(map[string]any)["id"])
	otherThread := s.do(http.MethodPost, "/api/v1/threads", token, map[string]any{"web_search": false})
	if otherThread.status != 201 {
		t.Fatalf("JSON start: %d %s", otherThread.status, otherThread.body)
	}
	foreignThread := number(t, otherThread.json["thread"].(map[string]any)["id"])
	attached := s.do(http.MethodGet, threadPath(thread, "/files"), token, nil)
	items := apiItems(t, attached)
	if len(items) != 1 {
		t.Fatalf("multipart thread start file missing: %s", attached.body)
	}
	threadFile := number(t, items[0].(map[string]any)["id"])
	threadURL := threadPath(thread, "/files/"+strconv.FormatInt(threadFile, 10))
	if read := s.do(http.MethodGet, threadURL+"/content", token, nil); read.status != 200 || read.body != "thread A only" {
		t.Fatalf("thread download: %d %s", read.status, read.body)
	}
	if read := s.do(http.MethodHead, threadURL+"/content", token, nil, "Range", "bytes=0-5"); read.status != 206 || read.body != "" || read.header.Get("Content-Length") != "6" {
		t.Fatalf("thread HEAD Range: %d %s %v", read.status, read.body, read.header)
	}
	if len(apiItems(t, s.do(http.MethodGet, threadPath(foreignThread, "/files"), token, nil))) != 0 {
		t.Fatal("thread file leaked into foreign list")
	}
	for _, suffix := range []string{"", "/content"} {
		expectAPI(t, s.do(http.MethodGet, threadPath(foreignThread, "/files/"+strconv.FormatInt(threadFile, 10)+suffix), token, nil), 404, "not_found")
		expectAPI(t, s.do(http.MethodGet, threadPath(thread, "/files/"+strconv.FormatInt(file, 10)+suffix), token, nil), 404, "not_found")
	}
	expectAPI(t, s.do(http.MethodPost, threadPath(thread, "/files"), token, nil), 405, "method_not_allowed")
	model.Push(anthropictest.Reply("end_turn", anthropictest.Text("read notes")))
	body, media = uploadBody(t, map[string]string{"text": "Read both notes."}, "second.txt", "second attachment")
	sent := s.do(http.MethodPost, threadPath(thread, "/messages"), token, body, "Content-Type", media)
	if sent.status != 202 {
		t.Fatalf("multipart send: %d %s", sent.status, sent.body)
	}
	waitThreadSettled(t, s, token, thread)
	if files := s.do(http.MethodGet, threadPath(thread, "/files"), token, nil); len(apiItems(t, files)) != 2 {
		t.Fatalf("send attachment missing: %s", files.body)
	}

	body, media = uploadBody(t, nil, "large.txt", strings.Repeat("x", (32<<20)+1))
	expectAPI(t, s.do(http.MethodPost, projectPath(project, "/files"), token, body, "Content-Type", media), 413, "too_large")
	if deleted := s.do(http.MethodDelete, fileURL, token, nil); deleted.status != 204 {
		t.Fatalf("delete: %d %s", deleted.status, deleted.body)
	}
	expectAPI(t, s.do(http.MethodGet, fileURL, token, nil), 404, "not_found")
	archived := s.do(http.MethodPost, projectPath(project, "/archive"), token, map[string]any{"archived": true})
	if archived.status != 200 {
		t.Fatalf("archive: %d %s", archived.status, archived.body)
	}
	if view := s.do(http.MethodGet, projectPath(project, ""), token, nil); view.json["project"].(map[string]any)["archived"] != true {
		t.Fatalf("archive not visible: %s", view.body)
	}
	body, media = uploadBody(t, nil, "refused.txt", "no")
	expectAPI(t, s.do(http.MethodPost, projectPath(project, "/files"), token, body, "Content-Type", media), 409, "project_archived")
	for i := range 105 {
		createHTMLProject(t, base, fmt.Sprintf("Page %03d", i))
	}
	token = localAPIToken(t, "project-pages")
	first = s.do(http.MethodGet, "/api/v1/projects?limit=100", token, nil)
	if len(apiItems(t, first)) != 100 {
		t.Fatalf("project first page: %s", first.body)
	}
	cursor = first.json["next_cursor"].(string)
	next = s.do(http.MethodGet, "/api/v1/projects?limit=100&cursor="+url.QueryEscape(cursor), token, nil)
	if len(apiItems(t, next)) != 6 || next.json["next_cursor"] != nil {
		t.Fatalf("project next page: %s", next.body)
	}
	expectAPI(t, s.do(http.MethodGet, "/api/v1/projects?archived=true&cursor="+url.QueryEscape(cursor), token, nil), 400, "bad_cursor")
	archivedList := s.do(http.MethodGet, "/api/v1/projects?archived=true", token, nil)
	if len(apiItems(t, archivedList)) != 1 || number(t, apiItems(t, archivedList)[0].(map[string]any)["id"]) != project {
		t.Fatalf("archived list: %s", archivedList.body)
	}
}

func createHTMLProject(t *testing.T, base, name string) {
	t.Helper()
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
	form := url.Values{"name": {name}, "effort": {"high"}, "context_limit": {"200000"}}
	req, err := http.NewRequest(http.MethodPost, base+"/projects", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(csrf)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	req.Header.Set(web.CSRFHeader, csrf.Value)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("bulk project fixture: %d %s", resp.StatusCode, data)
	}
}
