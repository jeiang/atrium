package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/web"
)

type asstWeb struct {
	t      *testing.T
	base   string
	client *http.Client
	token  string
}

type asstFile struct {
	name, contentType string
	data              []byte
}

func asstBrowse(t *testing.T, base string) *asstWeb {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &asstWeb{t: t, base: base, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if code, body, _ := w.get("/chat", false); code != http.StatusOK {
		t.Fatalf("GET /chat = %d: %s", code, body)
	}
	u, _ := url.Parse(base)
	for _, c := range jar.Cookies(u) {
		if c.Name == web.CSRFCookie {
			w.token = c.Value
		}
	}
	if w.token == "" {
		t.Fatal("no csrf cookie")
	}
	return w
}

func (w *asstWeb) send(method, path string, body io.Reader, contentType string, hx bool, headers ...string) (int, string, http.Header) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.base+path, body)
	if err != nil {
		w.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if w.token != "" {
		req.Header.Set(web.CSRFHeader, w.token)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := w.client.Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp.StatusCode, string(b), resp.Header
}

func (w *asstWeb) get(path string, hx bool, headers ...string) (int, string, http.Header) {
	w.t.Helper()
	return w.send(http.MethodGet, path, nil, "", hx, headers...)
}

func (w *asstWeb) post(path string, form url.Values, hx bool) (int, string, http.Header) {
	w.t.Helper()
	return w.send(http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", hx)
}

func asstMultipart(t *testing.T, fields map[string]string, files []asstFile) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="upload"; filename="`+strings.ReplaceAll(f.name, `"`, `\"`)+`"`)
		h.Set("Content-Type", f.contentType)
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(f.data)
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

func (w *asstWeb) upload(path string, fields map[string]string, files []asstFile, hx bool) (int, string, http.Header) {
	w.t.Helper()
	body, contentType := asstMultipart(w.t, fields, files)
	return w.send(http.MethodPost, path, body, contentType, hx)
}

func (w *asstWeb) want(code int, want int, body string, contains ...string) {
	w.t.Helper()
	if code != want {
		w.t.Fatalf("status %d, want %d: %s", code, want, body)
	}
	for _, s := range contains {
		if !strings.Contains(body, s) {
			w.t.Errorf("response lacks %q", s)
		}
	}
}

func (w *asstWeb) start(fields map[string]string) int64 {
	w.t.Helper()
	code, body, h := w.upload("/chat", fields, nil, true)
	if code != http.StatusNoContent || !strings.HasPrefix(h.Get("HX-Redirect"), "/chat/") {
		w.t.Fatalf("POST /chat = %d %q: %s", code, h.Get("HX-Redirect"), body)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(h.Get("HX-Redirect"), "/chat/"), 10, 64)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func asstLastRun(t *testing.T, d *db.DB, thread int64) int64 {
	t.Helper()
	var id int64
	if err := d.Read.QueryRow("SELECT COALESCE(MAX(id), 0) FROM runs WHERE thread_id = ?", thread).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func asstSettle(t *testing.T, d *db.DB, run int64) (string, string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var state string
		var errText *string
		if err := d.Read.QueryRow("SELECT state, error FROM runs WHERE id = ?", run).Scan(&state, &errText); err == nil && state != "running" {
			if errText == nil {
				return state, ""
			}
			return state, *errText
		}
	}
	t.Fatalf("run %d still running", run)
	return "", ""
}

func asstSettleThread(t *testing.T, d *db.DB, thread int64) string {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); asstLastRun(t, d, thread) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("thread %d never started a run", thread)
		}
	}
	state, _ := asstSettle(t, d, asstLastRun(t, d, thread))
	return state
}

func asstWaitRequests(t *testing.T, api *modelLog, n int) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); len(api.Requests()) < n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d model requests, want %d", len(api.Requests()), n)
		}
	}
}

func asstHold(t *testing.T, api *modelLog) func() {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	prev := api.RequestHook()
	api.SetOnRequest(func(r anthropictest.Request) {
		prev(r)
		<-gate
	})
	return release
}

func asstWaitCount(t *testing.T, d *db.DB, want int, query string, args ...any) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); asstCount(t, d, query, args...) != want; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s = %d, want %d", query, asstCount(t, d, query, args...), want)
		}
	}
}

func asstFrameIDs(body string) []string {
	var ids []string
	for _, line := range strings.Split(body, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func asstServe(t *testing.T) (string, *modelLog, *db.DB, *asstWeb) {
	t.Helper()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	return dir, api, d, asstBrowse(t, startServeIn(t, dir))
}

func TestAsstChatTurnStreamsEditsRegeneratesBranchesAndDeletes(t *testing.T) {
	t.Parallel()
	_, api, d, w := asstServe(t)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Thinking("pondering", "sig"), anthropictest.Text("Hello **there** zebrafish")))
	thread := w.start(map[string]string{"text": "firstmarker"})
	run := asstLastRun(t, d, thread)
	if state, _ := asstSettle(t, d, run); state != "done" {
		t.Fatalf("first run %s", state)
	}

	code, events, h := w.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run), false)
	w.want(code, http.StatusOK, events, "event: text\n")
	if h.Get("Content-Type") != "text/event-stream; charset=utf-8" || h.Get("Cache-Control") != "private, no-store, no-transform" || h.Get("X-Accel-Buffering") != "no" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("event stream headers %v", h)
	}
	if !strings.HasSuffix(events, "event: done\ndata: {\"state\": \"done\"}\n\n") {
		t.Errorf("stream end %q", events[max(0, len(events)-80):])
	}
	ids := asstFrameIDs(events)
	if len(ids) < 2 {
		t.Fatalf("frame ids %v", ids)
	}
	_, resumed, _ := w.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run), false, "Last-Event-ID", ids[len(ids)-2])
	if got := asstFrameIDs(resumed); len(got) != 1 || got[0] != ids[len(ids)-1] {
		t.Errorf("resumed frames %v, want only %s", got, ids[len(ids)-1])
	}

	api.Push(asstReply("Answer two"))
	code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "secondmarker", "edit": "1"}, nil, true)
	w.want(code, http.StatusOK, body, "secondmarker")
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("edited run %s", state)
	}
	_, page, _ := w.get(fmt.Sprintf("/chat/%d", thread), false)
	if !strings.Contains(page, "Answer two") {
		t.Errorf("the edit did not replace the answer")
	}

	api.Push(asstReply("Answer three"))
	before := asstLastRun(t, d, thread)
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/regenerate", thread), url.Values{}, true)
	w.want(code, http.StatusOK, body)
	for deadline := time.Now().Add(20 * time.Second); asstLastRun(t, d, thread) == before; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("regenerate started no run")
		}
	}
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("regenerated run %s", state)
	}
	_, page, _ = w.get(fmt.Sprintf("/chat/%d", thread), false)
	if !strings.Contains(page, "Answer three") || strings.Contains(page, "Answer two") {
		t.Errorf("regenerate did not replace the answer")
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread); n != 2 {
		t.Errorf("%d live messages after edit and regenerate, want 2", n)
	}
	asstWaitCount(t, d, 2, "SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", thread)

	var seq int64
	if err := d.Read.QueryRow("SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	code, body, h = w.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {strconv.FormatInt(seq, 10)}}, false)
	w.want(code, http.StatusSeeOther, body)
	child := h.Get("Location")
	code, page, _ = w.get(child, false)
	w.want(code, http.StatusOK, page, "Branched from “firstmarker”")
	childID, err := strconv.ParseInt(strings.TrimPrefix(child, "/chat/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", childID)
	if code, body, _ := w.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {"99"}}, false); code != http.StatusNotFound {
		t.Errorf("branch from a missing message: %d %s", code, body)
	}

	code, body, h = w.post(fmt.Sprintf("/chat/%d/delete", thread), url.Values{}, true)
	if code != http.StatusNoContent || h.Get("HX-Redirect") != "/chat" {
		t.Fatalf("delete: %d %q %s", code, h.Get("HX-Redirect"), body)
	}
	if code, _, _ := w.get(fmt.Sprintf("/chat/%d", thread), false); code != http.StatusNotFound {
		t.Errorf("deleted thread answers %d", code)
	}
}

func TestAsstAskOperatorParksTheRunUntilTheOperatorAnswers(t *testing.T) {
	t.Parallel()
	_, api, d, w := asstServe(t)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which account?"})), asstReply("done"))
	thread := w.start(map[string]string{"text": "check my accounts"})
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "parked" || errText != "" {
		t.Fatalf("asking run %s %q, want parked", state, errText)
	}
	_, page, _ := w.get(fmt.Sprintf("/chat/%d", thread), false)
	w.want(http.StatusOK, http.StatusOK, page, "Which account?")
	sent := len(api.Requests())
	code, body, _ := w.post(fmt.Sprintf("/chat/%d/answer", thread), url.Values{"text": {"personal"}}, true)
	w.want(code, http.StatusOK, body)
	asstWaitRequests(t, api, sent+1)
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("answered run %s", state)
	}
	resumed := string(api.Requests()[sent].Body)
	if !strings.Contains(resumed, "personal") || !strings.Contains(resumed, "tool_result") {
		t.Errorf("the resumed request does not carry the answer as a tool result")
	}
}

func TestAsstLiveRunQueuesStopsAndStaysResumableAcrossShutdown(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, stop := startServeStoppable(t, dir)
	w := asstBrowse(t, base)

	api.Push(asstReply("first"), asstReply("second"))
	release := asstHold(t, api)
	sent := len(api.Requests())
	queuedThread := w.start(map[string]string{"text": "start"})
	asstWaitRequests(t, api, sent+1)
	code, body, _ := w.upload(fmt.Sprintf("/chat/%d/messages", queuedThread), map[string]string{"text": "queued note"}, nil, true)
	w.want(code, http.StatusOK, body, "queued note")
	run := asstLastRun(t, d, queuedThread)
	release()
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("queued run %s %q", state, errText)
	}
	if requests := api.Requests(); len(requests) != sent+2 || !strings.Contains(string(requests[sent+1].Body), "queued note") {
		t.Fatalf("the queued note did not join the next step: %d requests", len(requests)-sent)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NOT NULL AND run_id = ?", queuedThread, run); n != 1 {
		t.Errorf("consumed queue rows %d, want 1", n)
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("p-stop", "profile_get", map[string]any{"key": "k"})))
	release = asstHold(t, api)
	sent = len(api.Requests())
	stopped := w.start(map[string]string{"text": "long job"})
	asstWaitRequests(t, api, sent+1)
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/regenerate", stopped), url.Values{}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Still working")
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/stop", stopped), url.Values{}, false)
	w.want(code, http.StatusNoContent, body)
	release()
	if state, errText := asstSettle(t, d, asstLastRun(t, d, stopped)); state != "failed" || !strings.Contains(errText, "stop") {
		t.Fatalf("stopped run %s %q", state, errText)
	}
	if code, _, _ := w.post(fmt.Sprintf("/chat/%d/stop", stopped), url.Values{}, false); code != http.StatusConflict {
		t.Errorf("second stop %d, want 409", code)
	}
	if len(api.Requests()) != sent+1 {
		t.Errorf("the stopped run kept calling the model")
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("outlive", "profile_get", map[string]any{"key": "k"})))
	release = asstHold(t, api)
	sent = len(api.Requests())
	busy := w.start(map[string]string{"text": "outlive shutdown"})
	asstWaitRequests(t, api, sent+1)
	stopWhileHeld(t, stop, release)
	var state string
	if err := d.Read.QueryRow("SELECT state FROM runs WHERE id = ?", asstLastRun(t, d, busy)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" && state != "interrupted" {
		t.Errorf("run in flight at shutdown ended %s, want it left resumable", state)
	}
}

func TestAsstUploadsAreLimitedAndServedSafely(t *testing.T) {
	t.Parallel()
	_, api, d, w := asstServe(t)
	live := "SELECT COUNT(*) FROM threads WHERE deleted_at IS NULL"
	threads := asstCount(t, d, live)

	body, contentType := asstMultipart(t, map[string]string{"text": "big"}, []asstFile{
		{"a.bin", "application/octet-stream", make([]byte, 32<<20)},
		{"b.bin", "application/octet-stream", make([]byte, 32<<20)},
		{"c.bin", "application/octet-stream", make([]byte, 2<<20)},
	})
	code, resp, _ := w.send(http.MethodPost, "/chat", struct{ io.Reader }{body}, contentType, true)
	w.want(code, http.StatusUnprocessableEntity, resp, "files in one message must total under 64 MB")
	code, resp, _ = w.upload("/chat", map[string]string{"text": "big"}, []asstFile{{"big.txt", "text/plain", make([]byte, 32<<20+1)}}, true)
	w.want(code, http.StatusUnprocessableEntity, resp, "big.txt is over the 32 MB limit")
	code, resp, _ = w.upload("/chat", nil, []asstFile{{"a.txt", "text/plain", []byte("hi")}}, true)
	w.want(code, http.StatusUnprocessableEntity, resp, "Type a message to go with the files.")
	many := map[string]string{"text": "hi"}
	for i := range 1001 {
		many[fmt.Sprintf("f%d", i)] = "x"
	}
	if code, _, _ := w.upload("/chat", many, nil, true); code != http.StatusBadRequest {
		t.Errorf("1001 parts: %d, want 400", code)
	}
	if code, _, _ := w.upload("/chat", map[string]string{"text": strings.Repeat("x", 1<<20+1)}, nil, true); code != http.StatusBadRequest {
		t.Errorf("a field over 1 MiB: %d, want 400", code)
	}
	if n := asstCount(t, d, live); n != threads {
		t.Fatalf("refused uploads created %d threads", n-threads)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files"); n != 0 {
		t.Fatalf("refused uploads stored %d files", n)
	}

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	api.Push(asstReply("read them"))
	owner := w.startFiles(map[string]string{"text": "look"}, []asstFile{
		{`report "q" 1.html`, "text/html", []byte("<script>alert(1)</script>")},
		{"pixel.png", "image/png", png},
		{"secret.txt", "text/plain", []byte("TOPSECRET")},
	})
	if state := asstSettleThread(t, d, owner); state != "done" {
		t.Fatalf("upload run %s", state)
	}
	fileID := func(name string) int64 {
		var id int64
		if err := d.Read.QueryRow("SELECT id FROM thread_files WHERE thread_id = ? AND name = ?", owner, name).Scan(&id); err != nil {
			t.Fatalf("file %s: %v", name, err)
		}
		return id
	}
	html, image := fileID(`report "q" 1.html`), fileID("pixel.png")
	code, _, h := w.get(fmt.Sprintf("/chat/%d/files/%d/download", owner, html), false)
	if code != http.StatusOK || h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Disposition") != `attachment; filename*=utf-8''report%20%22q%22%201.html` {
		t.Errorf("html download %d %v", code, h)
	}
	if code, _, _ := w.get(fmt.Sprintf("/chat/%d/files/%d/raw", owner, html), false); code != http.StatusNotFound {
		t.Errorf("html served inline: %d", code)
	}
	if code, _, h := w.get(fmt.Sprintf("/chat/%d/files/%d/raw", owner, image), false); code != http.StatusOK || h.Get("Content-Type") != "image/png" {
		t.Errorf("png inline %d %v", code, h)
	}

	other := w.start(map[string]string{"text": ""})
	if code, _, _ := w.get(fmt.Sprintf("/chat/%d/files/%d/download", other, html), false); code != http.StatusNotFound {
		t.Errorf("another thread downloads the file: %d", code)
	}
	var stored string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = ? AND role = 'user' ORDER BY seq LIMIT 1", owner).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(stored), &blocks); err != nil {
		t.Fatal(err)
	}
	foreign, err := json.Marshal(append(blocks, json.RawMessage(`{"type":"text","text":"earlier"}`)))
	if err != nil {
		t.Fatal(err)
	}
	asstExec(t, d, "INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', ?, 0)", other, string(foreign))
	api.Push(asstReply("nothing"))
	sent := len(api.Requests())
	code, resp, _ = w.upload(fmt.Sprintf("/chat/%d/messages", other), map[string]string{"text": "read it"}, nil, true)
	w.want(code, http.StatusOK, resp)
	asstWaitRequests(t, api, sent+1)
	asstSettleThread(t, d, other)
	if leaked := string(api.Requests()[sent].Body); strings.Contains(leaked, "TOPSECRET") || !strings.Contains(leaked, "is no longer available") {
		t.Errorf("a file from another thread reached the model")
	}
}

func (w *asstWeb) startFiles(fields map[string]string, files []asstFile) int64 {
	w.t.Helper()
	code, body, h := w.upload("/chat", fields, files, true)
	if code != http.StatusNoContent {
		w.t.Fatalf("POST /chat with files = %d: %s", code, body)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(h.Get("HX-Redirect"), "/chat/"), 10, 64)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func asstThreadRow(t *testing.T, d *db.DB, id int64) (project *int64, effort string, limit int64) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT project_id, effort, context_limit FROM threads WHERE id = ?", id).Scan(&project, &effort, &limit); err != nil {
		t.Fatal(err)
	}
	return project, effort, limit
}

func TestAsstProjectsGiveDefaultsAndArchivedProjectsRefuseNewContent(t *testing.T) {
	t.Parallel()
	_, _, d, w := asstServe(t)

	code, body, h := w.post("/settings/assistant", url.Values{"effort": {"high"}, "context_limit": {"200000"}}, false)
	if code != http.StatusSeeOther || h.Get("Location") != "/settings/assistant?saved=1" {
		t.Fatalf("assistant defaults: %d %q %s", code, h.Get("Location"), body)
	}
	code, body, _ = w.post("/settings/assistant", url.Values{"effort": {"ultra"}, "context_limit": {"200000"}}, false)
	w.want(code, http.StatusUnprocessableEntity, body, "effort must be one of low, medium, high, xhigh, max")
	loose := w.start(map[string]string{"text": ""})
	if project, effort, limit := asstThreadRow(t, d, loose); project != nil || effort != "high" || limit != 200_000 {
		t.Errorf("new thread took %v %s %d, want the saved defaults", project, effort, limit)
	}

	code, body, h = w.post("/projects", url.Values{"name": {"Kitchen"}, "instructions": {"Always answer in metric units."}, "effort": {"xhigh"}, "context_limit": {"500000"}}, false)
	if code != http.StatusSeeOther || !strings.HasSuffix(h.Get("Location"), "/edit") {
		t.Fatalf("create project: %d %q %s", code, h.Get("Location"), body)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(h.Get("Location"), "/projects/"), "/edit")
	note := asstFile{"note.md", "text/markdown", []byte("# Note\nhello")}
	code, body, _ = w.upload("/projects/"+id+"/files", nil, []asstFile{note}, true)
	w.want(code, http.StatusOK, body, "note.md")
	code, body, _ = w.upload("/projects/"+id+"/files", nil, []asstFile{{"", "text/plain", []byte("nameless")}, {"empty.txt", "text/plain", nil}}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Choose at least one file.")
	var fileID int64
	if err := d.Read.QueryRow("SELECT id FROM thread_files WHERE project_id = ?", id).Scan(&fileID); err != nil {
		t.Fatal(err)
	}

	inside := w.start(map[string]string{"text": "", "project_id": id})
	if project, effort, limit := asstThreadRow(t, d, inside); project == nil || strconv.FormatInt(*project, 10) != id || effort != "xhigh" || limit != 500_000 {
		t.Errorf("thread in the project took %v %s %d, want the project defaults", project, effort, limit)
	}
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/project", loose), url.Values{"project_id": {id}}, false)
	w.want(code, http.StatusSeeOther, body)
	if project, _, _ := asstThreadRow(t, d, loose); project == nil {
		t.Fatal("the thread did not move into the project")
	}
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/project", loose), url.Values{"project_id": {"0"}}, false)
	w.want(code, http.StatusSeeOther, body)
	if project, _, _ := asstThreadRow(t, d, loose); project != nil {
		t.Fatal("the thread did not move out of the project")
	}

	code, body, _ = w.post("/projects/"+id+"/archive", url.Values{"archived": {"1"}}, false)
	w.want(code, http.StatusSeeOther, body)
	files := asstCount(t, d, "SELECT COUNT(*) FROM thread_files")
	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	code, body, _ = w.upload("/projects/"+id+"/files", nil, []asstFile{note}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "“Kitchen” is archived. Restore it to add files.")
	code, body, _ = w.post(fmt.Sprintf("/projects/%s/files/%d/delete", id, fileID), url.Values{}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Restore it to remove files.")
	code, body, _ = w.upload("/chat", map[string]string{"text": "hello", "project_id": id}, []asstFile{note}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Project archived", "Restore it to start chats in it.")
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/project", loose), url.Values{"project_id": {id}}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Restore it to move chats into it.")
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/branch", inside), url.Values{"seq": {"1"}}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Project archived")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files"); n != files {
		t.Errorf("archived project files changed: %d, want %d", n, files)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads {
		t.Errorf("archived project threads changed: %d, want %d", n, threads)
	}
	if project, _, _ := asstThreadRow(t, d, loose); project != nil {
		t.Error("a thread moved into the archived project")
	}
	if code, _, _ := w.get(fmt.Sprintf("/projects/%s/files/%d/download", id, fileID), false); code != http.StatusOK {
		t.Errorf("archived project file download %d", code)
	}

	code, body, _ = w.post("/projects/"+id+"/archive", url.Values{"archived": {"0"}}, false)
	w.want(code, http.StatusSeeOther, body)
	code, body, _ = w.upload("/projects/"+id+"/files", nil, []asstFile{note}, true)
	w.want(code, http.StatusOK, body)
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/project", loose), url.Values{"project_id": {id}}, false)
	w.want(code, http.StatusSeeOther, body)
	code, body, _ = w.post(fmt.Sprintf("/projects/%s/files/%d/delete", id, fileID), url.Values{}, true)
	w.want(code, http.StatusOK, body)
}

func TestAsstThreadModelIsPricedPerThreadAndADownSwitchDropsThinking(t *testing.T) {
	t.Parallel()
	_, api, d, w := asstServe(t)
	api.Push(asstReply("sonnet here"), anthropictest.Reply("end_turn", anthropictest.Thinking("deep thought", "sig"), anthropictest.Text("opus here")))

	sent := len(api.Requests())
	thread := w.start(map[string]string{"text": "hi", "model": pricing.SonnetID, "effort": "low", "context_limit": "200000"})
	run := asstLastRun(t, d, thread)
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("sonnet run %s %q", state, errText)
	}
	if got := api.Requests()[sent].JSON(t)["model"]; got != pricing.SonnetID {
		t.Errorf("request model %v", got)
	}
	var model, tier string
	var cost float64
	if err := d.Read.QueryRow("SELECT model, tier, cost_usd FROM usage WHERE run_id = ?", run).Scan(&model, &tier, &cost); err != nil {
		t.Fatal(err)
	}
	if want := (1000*2.0 + 100*10.0) / 1e6; model != pricing.SonnetID || tier != "flat" || cost != want {
		t.Errorf("sonnet usage %s %s %v, want flat %v", model, tier, cost, want)
	}

	code, body, _ := w.post(fmt.Sprintf("/chat/%d/options", thread), url.Values{"model": {pricing.OpusID}, "effort": {"high"}, "context_limit": {"500000"}}, true)
	w.want(code, http.StatusOK, body)
	code, body, _ = w.upload(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "again"}, nil, true)
	w.want(code, http.StatusOK, body)
	asstWaitRequests(t, api, sent+2)
	run = asstLastRun(t, d, thread)
	if state, errText := asstSettle(t, d, run); state != "done" {
		t.Fatalf("opus run %s %q", state, errText)
	}
	if got := api.Requests()[sent+1].JSON(t)["model"]; got != pricing.OpusID {
		t.Errorf("second request model %v", got)
	}
	if err := d.Read.QueryRow("SELECT cost_usd FROM usage WHERE run_id = ?", run).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if want := (1000*4.0 + 100*20.0) / 1e6; cost != want {
		t.Errorf("opus cost %v, want %v", cost, want)
	}

	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	code, body, _ = w.upload("/chat", map[string]string{"text": "hi", "model": "claude-fable-5-1"}, nil, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Not started")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads {
		t.Errorf("a refused model created a thread")
	}
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/options", thread), url.Values{"model": {"gpt-5"}, "effort": {"low"}, "context_limit": {"90000"}}, true)
	w.want(code, http.StatusUnprocessableEntity, body, "Not saved")

	thinking := `SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND role = 'assistant' AND content_json LIKE '%"type":"thinking"%'`
	if n := asstCount(t, d, thinking, thread); n != 1 {
		t.Fatalf("%d stored thinking blocks before the switch, want 1", n)
	}
	code, body, _ = w.post(fmt.Sprintf("/chat/%d/options", thread), url.Values{"model": {pricing.SonnetID}, "effort": {"low"}, "context_limit": {"200000"}}, true)
	w.want(code, http.StatusOK, body)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads WHERE id = ? AND model = ?", thread, pricing.SonnetID); n != 1 {
		t.Fatal("the switch back to sonnet was not saved")
	}
	if n := asstCount(t, d, thinking, thread); n != 0 {
		t.Errorf("a down-switch kept %d thinking blocks", n)
	}
}

func TestAsstTodayOpensOneThreadUnderConcurrentVisits(t *testing.T) {
	t.Parallel()
	_, _, d, w := asstServe(t)
	locations := make([]string, 8)
	var wg sync.WaitGroup
	for i := range locations {
		wg.Go(func() {
			req, err := http.NewRequest(http.MethodGet, w.base+"/today", nil)
			if err != nil {
				return
			}
			resp, err := w.client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusSeeOther {
				locations[i] = resp.Header.Get("Location")
			}
		})
	}
	wg.Wait()
	for _, at := range locations {
		if at == "" || at != locations[0] || !strings.HasPrefix(at, "/chat/") {
			t.Fatalf("today locations %q", locations)
		}
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads WHERE role = 'today'"); n != 1 {
		t.Errorf("%d today threads, want 1", n)
	}
}

func TestAsstScheduledJobsToggleCancelAndProtectBuiltins(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("s1", "schedule_create", map[string]any{"name": "Digest", "prompt": "Summarize mail.", "schedule": "weekly mon 08:00"})), asstReply("scheduled"))
	if v, _ := asstRun(t, "--text", "schedule a weekly digest"); v.State != "done" {
		t.Fatalf("schedule run %+v %q", v, v.err())
	}
	jobID := func(name string) string {
		var id int64
		if err := d.Read.QueryRow("SELECT id FROM jobs WHERE name = ?", name).Scan(&id); err != nil {
			t.Fatalf("job %s: %v", name, err)
		}
		return strconv.FormatInt(id, 10)
	}
	column := func(col, id string) string {
		var out string
		if err := d.Read.QueryRow("SELECT "+col+" FROM jobs WHERE id = ?", id).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	state := func(id string) string { return column("state", id) }
	audits := func(operation, outcome, target string) int {
		return asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = ? AND outcome = ? AND target = ?", operation, outcome, target)
	}
	w := asstBrowse(t, startServeIn(t, dir))
	code, page, _ := w.get("/scheduled", false)
	w.want(code, http.StatusOK, page, "Digest", "weekly-review")
	digest, nightly := jobID("Digest"), jobID("nightly")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit a JOIN effects e ON a.details_json = '{\"effect_ids\":[' || e.id || ']}' "+
		"WHERE a.operation = 'assistant.schedule.create' AND a.outcome = 'ok' AND a.target = ? AND e.operation = 'schedule_create'", "schedule:"+digest); n != 1 {
		t.Errorf("schedule create audits naming its effect = %d, want 1", n)
	}
	digestID, _ := strconv.ParseInt(digest, 10, 64)
	nightlyID, _ := strconv.ParseInt(nightly, 10, 64)
	api.Push(
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s2", "schedule_update", map[string]any{"id": digestID, "prompt": "Summarize mail and tasks."})),
		anthropictest.Reply("tool_use", anthropictest.ToolUse("s3", "schedule_cancel", map[string]any{"id": nightlyID})),
		asstReply("updated"))
	if v, _ := asstRun(t, "--text", "widen the digest and stop the nightly run"); v.State != "done" {
		t.Fatalf("update run %+v %q", v, v.err())
	}
	if column("prompt", digest) != "Summarize mail and tasks." || state(nightly) != "active" {
		t.Errorf("agent update prompt %q, nightly %s", column("prompt", digest), state(nightly))
	}
	if audits("assistant.schedule.update", "ok", "schedule:"+digest) != 1 || audits("assistant.schedule.cancel", "refused", "schedule:"+nightly) != 1 {
		t.Error("agent update and refused built-in cancel are not audited")
	}
	var updateEffect int64
	if err := d.Read.QueryRow("SELECT id FROM effects WHERE operation = 'schedule_update' AND target = ?", "schedule:"+digest).Scan(&updateEffect); err != nil {
		t.Fatal(err)
	}
	if got := runApp(t, "effects", "undo", strconv.FormatInt(updateEffect, 10)); got.code != cli.ExitOK {
		t.Fatalf("effects undo: %d %q %q", got.code, got.stdout, got.stderr)
	}
	if column("prompt", digest) != "Summarize mail." {
		t.Errorf("undo left prompt %q", column("prompt", digest))
	}

	code, body, _ := w.post("/scheduled/"+digest+"/toggle", url.Values{}, true)
	w.want(code, http.StatusOK, body)
	if state(digest) == "active" {
		t.Error("toggle left the job active")
	}
	code, body, h := w.post("/scheduled/"+digest+"/toggle", url.Values{}, false)
	if code != http.StatusSeeOther || h.Get("Location") != "/scheduled" {
		t.Errorf("no-JS toggle: %d %q %s", code, h.Get("Location"), body)
	}
	if state(digest) != "active" {
		t.Error("second toggle did not enable the job")
	}

	weekly := jobID("weekly-review")
	code, body, _ = w.post("/scheduled/"+weekly+"/cancel", url.Values{}, true)
	w.want(code, http.StatusUnprocessableEntity, body)
	if state(weekly) == "cancelled" {
		t.Error("a built-in run was cancelled")
	}

	code, body, _ = w.post("/scheduled/"+digest+"/cancel", url.Values{}, true)
	w.want(code, http.StatusOK, body)
	if state(digest) != "cancelled" {
		t.Errorf("cancelled job state %s", state(digest))
	}
	if code, _, _ := w.post("/scheduled/"+digest+"/toggle", url.Values{}, false); code != http.StatusNotFound {
		t.Errorf("toggle of a cancelled job: %d, want 404", code)
	}
	schedule := func(id string) string { return column("schedule", id) }
	if code, body, _ := w.post("/scheduled/"+nightly+"/schedule", url.Values{"schedule": {"daily 03:15"}}, true); code != http.StatusOK || schedule(nightly) != "daily 03:15" {
		t.Errorf("edit the nightly schedule = %d, stored %q: %.200s", code, schedule(nightly), body)
	}
	if row := runSettings(t, "get", "agent_schedule_nightly").row(t); row.Value == nil || *row.Value != "daily 03:15" {
		t.Errorf("agent_schedule_nightly = %+v, want the edited schedule", row)
	}
	if code, _, _ := w.post("/scheduled/"+nightly+"/schedule", url.Values{"schedule": {"whenever"}}, true); code != http.StatusUnprocessableEntity || schedule(nightly) != "daily 03:15" {
		t.Errorf("an invalid schedule = %d, stored %q, want a refusal that keeps the old one", code, schedule(nightly))
	}
	for _, want := range []struct {
		operation, outcome, target string
		n                          int
	}{
		{"assistant.schedule.enabled", "ok", "schedule:" + digest, 2},
		{"assistant.schedule.enabled", "not_found", "schedule:" + digest, 1},
		{"assistant.schedule.cancel", "refused", "schedule:" + weekly, 1},
		{"assistant.schedule.cancel", "ok", "schedule:" + digest, 1},
		{"assistant.schedule.update", "ok", "schedule:" + nightly, 1},
		{"assistant.schedule.update", "invalid", "schedule:" + nightly, 1},
	} {
		if n := audits(want.operation, want.outcome, want.target); n != want.n {
			t.Errorf("%s %s %s audits = %d, want %d", want.operation, want.outcome, want.target, n, want.n)
		}
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation LIKE 'assistant.schedule.%' AND source = 'ui' AND actor = 'operator'"); n != 7 {
		t.Errorf("operator schedule audits = %d, want 7", n)
	}
}

func TestBranchKeepsRunOptions(t *testing.T) {
	_, api, d, w := asstServe(t)
	api.Push(asstReply("branch source answer"))
	thread := w.start(map[string]string{"text": "branch options", "web_search": "0", "cost_cap": "2.5"})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("source run %s", state)
	}
	var seq int64
	if err := d.Read.QueryRow("SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	code, body, h := w.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {strconv.FormatInt(seq, 10)}}, false)
	w.want(code, http.StatusSeeOther, body)
	child, err := strconv.ParseInt(strings.TrimPrefix(h.Get("Location"), "/chat/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	options := func(id int64) string {
		var model, effort, thinking string
		var limit, search int64
		var costCap float64
		if err := d.Read.QueryRow("SELECT model, effort, COALESCE(context_limit, 0), thinking, web_search, COALESCE(cost_cap_usd, 0) FROM threads WHERE id = ?", id).
			Scan(&model, &effort, &limit, &thinking, &search, &costCap); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s|%s|%d|%s|%d|%g", model, effort, limit, thinking, search, costCap)
	}
	source, branched := options(thread), options(child)
	if source != branched || !strings.HasSuffix(source, "|0|2.5") {
		t.Fatalf("branch options %q, source %q (want web search off and the 2.5 cap kept)", branched, source)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = 'assistant.thread.branch' AND outcome = 'ok' AND target = ?", fmt.Sprintf("thread:%d", child)); n != 1 {
		t.Fatalf("%d branch audits for the child, want 1", n)
	}
}

func TestProjectFilesStayInTheirProjectAndAreAudited(t *testing.T) {
	t.Parallel()
	_, _, d, w := asstServe(t)
	project := func(name string) string {
		code, body, h := w.post("/projects", url.Values{"name": {name}, "effort": {"high"}, "context_limit": {"200000"}}, false)
		if code != http.StatusSeeOther {
			t.Fatalf("create project %s: %d %s", name, code, body)
		}
		return strings.TrimSuffix(strings.TrimPrefix(h.Get("Location"), "/projects/"), "/edit")
	}
	home, other := project("Home"), project("Other")
	code, body, _ := w.upload("/projects/"+home+"/files", nil, []asstFile{{"plan.txt", "text/plain", []byte("the plan")}}, true)
	if code != http.StatusOK {
		t.Fatalf("upload: %d %s", code, body)
	}
	var fileID int64
	var stored string
	if err := d.Read.QueryRow("SELECT id, path FROM thread_files WHERE project_id = ?", home).Scan(&fileID, &stored); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := w.get(fmt.Sprintf("/projects/%s/files/%d/download", home, fileID), false); code != http.StatusOK || body != "the plan" {
		t.Fatalf("own project download: %d %q", code, body)
	}
	for _, path := range []string{
		fmt.Sprintf("/projects/%s/files/%d/download", other, fileID),
		fmt.Sprintf("/projects/%s/files/%d/download", home, fileID+1000),
		fmt.Sprintf("/chat/1/files/%d/download", fileID),
	} {
		if code, _, _ := w.get(path, false); code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, code)
		}
	}
	if code, _, _ := w.post(fmt.Sprintf("/projects/%s/files/%d/delete", other, fileID), url.Values{}, true); code != http.StatusNotFound {
		t.Fatalf("delete through another project: %d, want 404", code)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files WHERE id = ?", fileID); n != 1 {
		t.Fatalf("a foreign delete removed the file row")
	}
	if code, body, _ := w.post(fmt.Sprintf("/projects/%s/files/%d/delete", home, fileID), url.Values{}, true); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files WHERE id = ?", fileID); n != 0 {
		t.Errorf("the deleted file row remains")
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Errorf("the deleted file remains on disk: %v", err)
	}
	for _, want := range []struct{ operation, outcome, project string }{
		{"assistant.project-file.upload", "ok", home},
		{"assistant.project-file.delete", "not_found", other},
		{"assistant.project-file.delete", "ok", home},
	} {
		if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = ? AND outcome = ? AND target = ?", want.operation, want.outcome, "project:"+want.project); n != 1 {
			t.Errorf("%s %s audits for project %s: %d, want 1", want.operation, want.outcome, want.project, n)
		}
	}
}
