package main

import (
	"bufio"
	"bytes"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/web"
)

func htmlRequest(t *testing.T, s *apiSite, client *http.Client, method, path, contentType string, body io.Reader, headers ...string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Remote-User", "owner")
	req.Header.Set("Remote-Groups", "atrium_access,atrium_admin")
	for _, c := range client.Jar.Cookies(req.URL) {
		req.AddCookie(c)
		if c.Name == web.CSRFCookie {
			req.Header.Set(web.CSRFHeader, c.Value)
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return req
}

func htmlDo(t *testing.T, req *http.Request) (int, string, http.Header) {
	t.Helper()
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(text), resp.Header
}

func composerBody(t *testing.T, text string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if err := mw.WriteField("text", text); err != nil {
		t.Fatal(err)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="upload"; filename=""`)
	h.Set("Content-Type", "application/octet-stream")
	if _, err := mw.CreatePart(h); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), mw.FormDataContentType()
}

func htmlThread(t *testing.T, s *apiSite, owner string) int64 {
	t.Helper()
	started := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "hello", "web_search": false})
	if started.status != http.StatusCreated {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	id := number(t, thread["id"])
	waitThreadSettled(t, s, owner, id)
	return id
}

func TestHTMLSendRetryAfterAbort(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	client := s.browser()
	api.Push(asstReply("started"), asstReply("aborted reply"), asstReply("spare"))
	id := htmlThread(t, s, owner)
	requests := len(api.Requests())
	path := "/chat/" + strconv.FormatInt(id, 10) + "/messages"
	body, contentType := composerBody(t, "aborted note")

	first := htmlRequest(t, s, client, http.MethodPost, path, contentType, bytes.NewReader(body), "Idempotency-Key", "abort-key")
	conn, err := net.Dial("tcp", first.URL.Host)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Write(conn); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	var status int
	var text string
	var header http.Header
	for deadline := time.Now().Add(20 * time.Second); ; {
		status, text, header = htmlDo(t, htmlRequest(t, s, client, http.MethodPost, path, contentType, bytes.NewReader(body), "Idempotency-Key", "abort-key"))
		if status != http.StatusConflict || !strings.Contains(text, "idempotency_in_progress") || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != http.StatusOK || header.Get("Atrium-Command-Id") == "" || !strings.Contains(text, "aborted note") {
		t.Fatalf("retry after abort: %d %q\n%s", status, header.Get("Atrium-Command-Id"), text)
	}
	waitThreadSettled(t, s, owner, id)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND role = 'user' AND superseded = 0 AND content_json LIKE '%aborted note%'", id); n != 1 {
		t.Fatalf("abort and retry stored %d messages, want 1", n)
	}
	if sent := len(api.Requests()) - requests; sent != 1 {
		t.Fatalf("abort and retry made %d provider requests, want 1", sent)
	}
}

func TestHTMLSendTextWhileUploadsHeld(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	client := s.browser()
	api.Push(asstReply("started"), asstReply("html reply"), asstReply("api reply"), asstReply("spare"))
	id := htmlThread(t, s, owner)

	const declared = 8 << 20
	held := func() {
		t.Helper()
		pr, pw := io.Pipe()
		boundary := "heldboundary"
		req := htmlRequest(t, s, client, http.MethodPost, "/chat/999999/messages", "multipart/form-data; boundary="+boundary, pr)
		req.ContentLength = declared
		conn, err := net.Dial("tcp", req.URL.Host)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pw.CloseWithError(io.ErrClosedPipe); _ = conn.Close() })
		go func() { _ = req.Write(conn) }()
		prefix := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"upload\"; filename=\"big.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n"
		if _, err := pw.Write([]byte(prefix + strings.Repeat("x", 64<<10))); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		held()
	}
	probe, probeType := asstMultipart(t, map[string]string{"text": "probe"}, []asstFile{{"probe.bin", "application/octet-stream", make([]byte, 2<<20)}})
	probeBody := probe.Bytes()
	for deadline := time.Now().Add(10 * time.Second); ; {
		status, text, _ := htmlDo(t, htmlRequest(t, s, client, http.MethodPost, "/chat/999998/messages", probeType, bytes.NewReader(probeBody)))
		if status == http.StatusTooManyRequests {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a fifth upload was not refused while four are held: %d %s", status, text)
		}
		time.Sleep(50 * time.Millisecond)
	}

	body, contentType := composerBody(t, "text while held")
	status, text, header := htmlDo(t, htmlRequest(t, s, client, http.MethodPost, "/chat/"+strconv.FormatInt(id, 10)+"/messages", contentType, bytes.NewReader(body), "Idempotency-Key", "held-html"))
	if status != http.StatusOK || header.Get("Atrium-Command-Id") == "" || !strings.Contains(text, "text while held") {
		t.Fatalf("HTML text send while uploads are held: %d\n%s", status, text)
	}

	apiBody, apiType := asstMultipart(t, map[string]string{"text": "api text while held"}, nil)
	req, err := http.NewRequest(http.MethodPost, s.base+"/api/v1/threads", apiBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", apiType)
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("Idempotency-Key", "held-api")
	status, text, _ = htmlDo(t, req)
	if status < 200 || status > 299 {
		t.Fatalf("API text send while uploads are held: %d %s", status, text)
	}
	if reply := s.do(http.MethodPost, threadPath(id, "/messages"), owner, map[string]any{"text": "json while held"}); reply.status < 200 || reply.status > 299 {
		t.Fatalf("API JSON send while uploads are held: %d %s", reply.status, reply.body)
	}
}

func TestHTMLSendRefusesOtherMediaAndOversize(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	client := s.browser()
	api.Push(asstReply("started"), asstReply("form reply"))
	id := htmlThread(t, s, owner)
	path := "/chat/" + strconv.FormatInt(id, 10) + "/messages"

	mixed := "--X\r\nContent-Disposition: form-data; name=\"text\"\r\n\r\nmixed\r\n--X--\r\n"
	for _, target := range []string{path, "/chat"} {
		for _, key := range []string{"", "mixed-key"} {
			status, text, _ := htmlDo(t, htmlRequest(t, s, client, http.MethodPost, target, "multipart/mixed; boundary=X", strings.NewReader(mixed), "Idempotency-Key", key))
			if status != http.StatusUnsupportedMediaType {
				t.Fatalf("multipart/mixed to %s (key %q): %d %s", target, key, status, text)
			}
		}
		status, text, _ := htmlDo(t, htmlRequest(t, s, client, http.MethodPost, target, "application/json", strings.NewReader(`{"text":"json"}`)))
		if status != http.StatusUnsupportedMediaType {
			t.Fatalf("JSON to %s: %d %s", target, status, text)
		}
	}

	req := htmlRequest(t, s, client, http.MethodPost, path, "multipart/form-data; boundary=X", http.NoBody, "Idempotency-Key", "oversize-key")
	conn, err := net.Dial("tcp", req.URL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var head strings.Builder
	head.WriteString("POST " + path + " HTTP/1.1\r\nHost: " + req.URL.Host + "\r\nContent-Length: " + strconv.Itoa(1<<30) + "\r\n")
	for name, values := range req.Header {
		for _, v := range values {
			head.WriteString(name + ": " + v + "\r\n")
		}
	}
	head.WriteString("\r\n")
	if _, err := conn.Write([]byte(head.String())); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(text), "files in one message must total under 64 MB") {
		t.Fatalf("declared oversize: %d %s", resp.StatusCode, text)
	}

	form := url.Values{"text": {"plain form"}}
	status, text2, _ := htmlDo(t, htmlRequest(t, s, client, http.MethodPost, "/chat", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())))
	if status >= 400 {
		t.Fatalf("urlencoded start: %d %s", status, text2)
	}
}

func TestHTMLSendKeysAreScoped(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	s := startAPISite(t, dir, api)
	owner, guest := s.token("owner"), s.token("guest")
	client := s.browser()
	api.Push(asstReply("owner"), asstReply("guest"), asstReply("form one"), asstReply("spare"))

	first := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "scoped key", "web_search": false}, "Idempotency-Key", "shared-key")
	if first.status != http.StatusCreated {
		t.Fatalf("owner start: %d %s", first.status, first.body)
	}
	other := s.do(http.MethodPost, "/api/v1/threads", guest, map[string]any{"text": "scoped key", "web_search": false}, "Idempotency-Key", "shared-key")
	if other.status != http.StatusCreated || other.header.Get("Idempotent-Replayed") != "" || other.header.Get("Atrium-Command-Id") == first.header.Get("Atrium-Command-Id") {
		t.Fatalf("another principal with the same key replayed: %d %q %s", other.status, other.header.Get("Idempotent-Replayed"), other.body)
	}
	again := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "scoped key", "web_search": false}, "Idempotency-Key", "shared-key")
	if again.status != http.StatusCreated || again.header.Get("Idempotent-Replayed") != "true" || again.header.Get("Atrium-Command-Id") != first.header.Get("Atrium-Command-Id") {
		t.Fatalf("same principal and key: %d %q %s", again.status, again.header.Get("Idempotent-Replayed"), again.body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE role = 'user' AND content_json LIKE '%scoped key%'"); n != 2 {
		t.Fatalf("two principals stored %d messages, want 2", n)
	}

	form := func(text string) (int, string) {
		t.Helper()
		req := htmlRequest(t, s, client, http.MethodPost, "/chat", "application/x-www-form-urlencoded", nil, "Idempotency-Key", "form-key")
		token := req.Header.Get(web.CSRFHeader)
		req.Header.Del(web.CSRFHeader)
		encoded := url.Values{"text": {text}, "csrf_token": {token}}.Encode()
		req.Body = io.NopCloser(strings.NewReader(encoded))
		req.ContentLength = int64(len(encoded))
		status, body, _ := htmlDo(t, req)
		return status, body
	}
	if status, body := form("form one"); status >= 400 {
		t.Fatalf("keyed form start: %d %s", status, body)
	}
	if status, body := form("form two"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "idempotency_mismatch") {
		t.Fatalf("same key, different form body: %d %s", status, body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE role = 'user' AND content_json LIKE '%form two%'"); n != 0 {
		t.Fatalf("a mismatched form body stored %d messages", n)
	}
}

func TestThreadRunDoneBadgeCountsDrafts(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	warm(t, api)
	d := asstDB(t, dir)
	for _, query := range []string{
		"INSERT INTO proposals (id, kind, status, created_ts, title, fingerprint) VALUES (901, 'reminder', 'pending', 100, 'Call the dentist', 'badge-fp')",
		"INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (901, 'a', 'pat@person.test', 'Re: lease', '', 'draft', 200, 200)",
		"INSERT INTO drafts (id, account, to_addrs, subject, body, status, created_at, updated_at) VALUES (902, 'a', 'sam@person.test', 'Re: rent', '', 'unknown', 201, 201)",
	} {
		if _, err := d.Write.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	s := startAPISite(t, dir, api)
	owner := s.token("owner")
	client := s.browser()
	api.Push(asstReply("started"))
	started := s.do(http.MethodPost, "/api/v1/threads", owner, map[string]any{"text": "badge", "web_search": false})
	if started.status != http.StatusCreated {
		t.Fatalf("start: %d %s", started.status, started.body)
	}
	thread, _ := started.json["thread"].(map[string]any)
	id, run := number(t, thread["id"]), number(t, started.json["run_id"])
	waitThreadSettled(t, s, owner, id)
	badge := regexp.MustCompile(`id="pending-count"[^>]*>(\d*)<`)
	count := func(page string) string {
		t.Helper()
		m := badge.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no pending badge:\n%s", page)
		}
		return m[1]
	}
	_, frame, _ := fetch(t, client, http.MethodGet, s.base+"/chat/"+strconv.FormatInt(id, 10), nil)
	status, body, _ := htmlDo(t, htmlRequest(t, s, client, http.MethodGet, "/chat/"+strconv.FormatInt(id, 10)+"/runs/"+strconv.FormatInt(run, 10), "", nil))
	if status != http.StatusOK || count(frame) != "3" || count(body) != count(frame) {
		t.Fatalf("finished run badge %q, page badge %q, want 3: %d\n%s", count(body), count(frame), status, body)
	}
}
