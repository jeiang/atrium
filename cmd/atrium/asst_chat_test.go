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
	_, api, d, w := webServeG1(t)
	api.Push(anthropictest.Reply("end_turn", anthropictest.Thinking("pondering", "sig"), anthropictest.Text("Hello **there** zebrafish")))
	thread := webStartG1(t, w, map[string]any{"text": "firstmarker"})
	run := asstLastRun(t, d, thread)
	if state, _ := asstSettle(t, d, run); state != "done" {
		t.Fatalf("first run %s", state)
	}

	stream := threadG1(thread, fmt.Sprintf("/runs/%d/events", run))
	got := w.do(http.MethodGet, stream, nil)
	webWantG1(t, got, http.StatusOK, "event: text\n")
	events, h := got.body, got.header
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
	resumed := w.do(http.MethodGet, stream, nil, "Last-Event-ID", ids[len(ids)-2])
	if got := asstFrameIDs(resumed.body); len(got) != 1 || got[0] != ids[len(ids)-1] {
		t.Errorf("resumed frames %v, want only %s", got, ids[len(ids)-1])
	}

	api.Push(asstReply("Answer two"))
	webWantG1(t, w.do(http.MethodPatch, threadG1(thread, "/messages/latest"), map[string]any{"text": "secondmarker"}), http.StatusAccepted)
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("edited run %s", state)
	}
	if page := w.do(http.MethodGet, threadG1(thread, ""), nil); !strings.Contains(page.body, "secondmarker") || !strings.Contains(page.body, "Answer two") {
		t.Errorf("the edit did not replace the answer: %.600s", page.body)
	}

	api.Push(asstReply("Answer three"))
	before := asstLastRun(t, d, thread)
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/regenerate"), nil), http.StatusAccepted)
	for deadline := time.Now().Add(20 * time.Second); asstLastRun(t, d, thread) == before; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("regenerate started no run")
		}
	}
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("regenerated run %s", state)
	}
	if page := w.do(http.MethodGet, threadG1(thread, ""), nil); !strings.Contains(page.body, "Answer three") || strings.Contains(page.body, "Answer two") {
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
	branched := w.do(http.MethodPost, threadG1(thread, "/branch"), map[string]any{"seq": seq})
	webWantG1(t, branched, http.StatusCreated)
	childID := number(t, branched.json["thread_id"])
	child := w.do(http.MethodGet, threadG1(childID, ""), nil)
	webWantG1(t, child, http.StatusOK, "firstmarker")
	if parent, _ := child.json["thread"].(map[string]any); parent == nil || number(t, parent["branched_from_thread_id"]) != thread {
		t.Errorf("the branch does not name its source: %.600s", child.body)
	}
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM memory_nodes WHERE thread_id = ? AND level = 0", childID)
	expectAPI(t, w.do(http.MethodPost, threadG1(thread, "/branch"), map[string]any{"seq": 99}), http.StatusConflict, "nothing_to_edit")

	webWantG1(t, w.do(http.MethodDelete, threadG1(thread, ""), nil), http.StatusNoContent)
	if got := w.do(http.MethodGet, threadG1(thread, ""), nil); got.status != http.StatusNotFound {
		t.Errorf("deleted thread answers %d", got.status)
	}
}

func TestAsstAskOperatorParksTheRunUntilTheOperatorAnswers(t *testing.T) {
	t.Parallel()
	_, api, d, w := webServeG1(t)
	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which account?"})), asstReply("done"))
	thread := webStartG1(t, w, map[string]any{"text": "check my accounts"})
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "parked" || errText != "" {
		t.Fatalf("asking run %s %q, want parked", state, errText)
	}
	webWantG1(t, w.do(http.MethodGet, threadG1(thread, ""), nil), http.StatusOK, "Which account?")
	sent := len(api.Requests())
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/answer"), map[string]any{"text": "personal"}), http.StatusAccepted)
	asstWaitRequests(t, api, sent+1)
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("answered run %s", state)
	}
	resumed := string(api.Requests()[sent].Body)
	if !strings.Contains(resumed, "personal") || !strings.Contains(resumed, "tool_result") {
		t.Errorf("the resumed request does not carry the answer as a tool result")
	}
}

func TestAsstStreamedReplyOutlivesTheConnectionTimeoutAndStopsPromptly(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, _ := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	held := asstReply("held")
	held.Trickle = 16 * time.Second
	api.Push(held)
	began := time.Now()
	thread := webStartG1(t, w, map[string]any{"text": "long reply"})
	if state, errText := asstSettle(t, d, asstLastRun(t, d, thread)); state != "done" {
		t.Fatalf("held run %s %q", state, errText)
	}
	if elapsed := time.Since(began); elapsed < 16*time.Second {
		t.Fatalf("the held run settled after %s, before its reply ended", elapsed)
	}

	stalled := asstReply("stalled")
	stalled.Trickle = 5 * time.Minute
	api.Push(stalled)
	sent := len(api.Requests())
	stopped := webStartG1(t, w, map[string]any{"text": "stop me"})
	asstWaitRequests(t, api, sent+1)
	webWantG1(t, w.do(http.MethodPost, threadG1(stopped, "/stop"), nil), http.StatusOK)
	asked := time.Now()
	if state, errText := asstSettle(t, d, asstLastRun(t, d, stopped)); state != "failed" || !strings.Contains(errText, "stop") {
		t.Fatalf("stopped run %s %q", state, errText)
	}
	if waited := time.Since(asked); waited > 5*time.Second {
		t.Fatalf("the stopped run settled %s after the stop", waited)
	}
	if len(api.Requests()) != sent+1 {
		t.Errorf("the stopped run kept calling the model")
	}
}

func TestAsstLiveRunQueuesStopsAndStaysResumableAcrossShutdown(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, stop := startServeStoppable(t, dir)
	w := localWebG1(t, base)

	api.Push(asstReply("first"), asstReply("second"))
	release := asstHold(t, api)
	sent := len(api.Requests())
	queuedThread := webStartG1(t, w, map[string]any{"text": "start"})
	asstWaitRequests(t, api, sent+1)
	queued := w.do(http.MethodPost, threadG1(queuedThread, "/messages"), map[string]any{"text": "queued note"})
	webWantG1(t, queued, http.StatusAccepted)
	if queued.json["queued_id"] == nil {
		t.Errorf("the note sent during a run was not queued: %s", queued.body)
	}
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
	stopped := webStartG1(t, w, map[string]any{"text": "long job"})
	asstWaitRequests(t, api, sent+1)
	expectAPI(t, w.do(http.MethodPost, threadG1(stopped, "/regenerate"), nil), http.StatusConflict, "run_active")
	webWantG1(t, w.do(http.MethodPost, threadG1(stopped, "/stop"), nil), http.StatusOK)
	release()
	if state, errText := asstSettle(t, d, asstLastRun(t, d, stopped)); state != "failed" || !strings.Contains(errText, "stop") {
		t.Fatalf("stopped run %s %q", state, errText)
	}
	expectAPI(t, w.do(http.MethodPost, threadG1(stopped, "/stop"), nil), http.StatusConflict, "no_active_run")
	if len(api.Requests()) != sent+1 {
		t.Errorf("the stopped run kept calling the model")
	}

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("outlive", "profile_get", map[string]any{"key": "k"})))
	release = asstHold(t, api)
	sent = len(api.Requests())
	busy := webStartG1(t, w, map[string]any{"text": "outlive shutdown"})
	asstWaitRequests(t, api, sent+1)
	stopWhileHeld(t, stop, release)
	var state string
	if err := d.Read.QueryRow("SELECT state FROM runs WHERE id = ?", asstLastRun(t, d, busy)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted" {
		t.Errorf("run in flight at shutdown ended %s, want it interrupted", state)
	}
}

func TestAsstRunStoppedByShutdownResumesAtOnceAfterRestart(t *testing.T) {
	t.Parallel()
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	base, _, stop := startServeStoppable(t, dir)
	asstExec(t, d, "UPDATE jobs SET next_due = NULL")
	w := localWebG1(t, base)

	api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("deploy", "profile_get", map[string]any{"key": "k"})))
	release := asstHold(t, api)
	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "outlive the deploy"})
	asstWaitRequests(t, api, sent+1)
	run := asstLastRun(t, d, thread)
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "note sent during the deploy"}), http.StatusAccepted)
	stopWhileHeld(t, stop, release)

	var state string
	var owner *string
	var lease *float64
	if err := d.Read.QueryRow("SELECT state, owner, lease_until FROM runs WHERE id = ?", run).Scan(&state, &owner, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted" || owner != nil || lease != nil {
		t.Fatalf("run stopped by shutdown is %s with owner %v and lease %v, want interrupted and released", state, owner, lease)
	}

	api.Push(asstReply("answered after the restart"))
	restarted := len(api.Requests())
	startServeIn(t, dir)
	asstWaitCount(t, d, 1, "SELECT COUNT(*) FROM runs WHERE id = ? AND state = 'done'", run)
	asstWaitCount(t, d, 0, "SELECT COUNT(*) FROM runs WHERE thread_id = ? AND state <> 'done'", thread)
	asstWaitCount(t, d, 0, "SELECT COUNT(*) FROM thread_queue WHERE thread_id = ? AND consumed_at IS NULL", thread)
	answered := false
	for _, r := range api.Requests()[restarted:] {
		answered = answered || strings.Contains(string(r.Body), "note sent during the deploy")
	}
	if !answered {
		t.Fatal("the note sent during the run was not answered after the restart")
	}
}

func TestAsstUploadsAreLimitedAndServedSafely(t *testing.T) {
	t.Parallel()
	_, api, d, w := webServeG1(t)
	live := "SELECT COUNT(*) FROM threads WHERE deleted_at IS NULL"
	threads := asstCount(t, d, live)

	expectAPI(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", map[string]string{"text": "big"}, []asstFile{
		{"a.bin", "application/octet-stream", make([]byte, 32<<20)},
		{"b.bin", "application/octet-stream", make([]byte, 32<<20)},
		{"c.bin", "application/octet-stream", make([]byte, 2<<20)},
	}), http.StatusRequestEntityTooLarge, "too_large")
	expectAPI(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", map[string]string{"text": "big"}, []asstFile{{"big.txt", "text/plain", make([]byte, 32<<20+1)}}), http.StatusRequestEntityTooLarge, "too_large")
	expectAPI(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", nil, []asstFile{{"a.txt", "text/plain", []byte("hi")}}), http.StatusUnprocessableEntity, "invalid")
	many := map[string]string{"text": "hi"}
	for i := range 1001 {
		many[fmt.Sprintf("f%d", i)] = "x"
	}
	expectAPI(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", many, nil), http.StatusUnprocessableEntity, "invalid")
	webWantG1(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", map[string]string{"text": strings.Repeat("x", 1<<20+1)}, nil), http.StatusUnprocessableEntity, "Multipart fields are too large.")
	if n := asstCount(t, d, live); n != threads {
		t.Fatalf("refused uploads created %d threads", n-threads)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files"); n != 0 {
		t.Fatalf("refused uploads stored %d files", n)
	}

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	api.Push(asstReply("read them"))
	started := webFormG1(t, w, http.MethodPost, "/web/v1/threads", map[string]string{"text": "look"}, []asstFile{
		{`report "q" 1.html`, "text/html", []byte("<script>alert(1)</script>")},
		{"pixel.png", "image/png", png},
		{"secret.txt", "text/plain", []byte("TOPSECRET")},
	})
	made, _ := started.json["thread"].(map[string]any)
	if started.status != http.StatusCreated || made == nil {
		t.Fatalf("start with files = %d: %.600s", started.status, started.body)
	}
	owner := number(t, made["id"])
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
	content := func(thread, file int64) string {
		return threadG1(thread, fmt.Sprintf("/files/%d/content", file))
	}
	got := w.do(http.MethodGet, content(owner, html), nil)
	if h := got.header; got.status != http.StatusOK || got.body != "<script>alert(1)</script>" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Disposition") != `attachment; filename="report \"q\" 1.html"` {
		t.Errorf("html download %d %v", got.status, h)
	}
	if got := w.do(http.MethodGet, content(owner, image), nil); got.status != http.StatusOK || got.header.Get("Content-Type") != "image/png" || got.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("png content %d %v", got.status, got.header)
	}

	other := webStartG1(t, w, map[string]any{})
	if got := w.do(http.MethodGet, content(other, html), nil); got.status != http.StatusNotFound {
		t.Errorf("another thread downloads the file: %d", got.status)
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
	webWantG1(t, webFormG1(t, w, http.MethodPost, threadG1(other, "/messages"), map[string]string{"text": "read it"}, nil), http.StatusAccepted)
	asstWaitRequests(t, api, sent+1)
	asstSettleThread(t, d, other)
	if leaked := string(api.Requests()[sent].Body); strings.Contains(leaked, "TOPSECRET") || !strings.Contains(leaked, "is no longer available") {
		t.Errorf("a file from another thread reached the model")
	}
}

func asstThreadRow(t *testing.T, d *db.DB, id int64) (project *int64, effort string, limit int64) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT project_id, effort, context_limit FROM threads WHERE id = ?", id).Scan(&project, &effort, &limit); err != nil {
		t.Fatal(err)
	}
	return project, effort, limit
}

func TestAsstSpacesGiveDefaultsAndArchivedSpacesRefuseNewContent(t *testing.T) {
	t.Parallel()
	_, _, d, w := webServeG1(t)

	webOKG1(t, w.do(http.MethodPut, "/web/v1/assistant/defaults", map[string]any{"effort": "high", "context_limit": 200000}))
	expectAPI(t, w.do(http.MethodPut, "/web/v1/assistant/defaults", map[string]any{"effort": "ultra", "context_limit": 200000}), http.StatusUnprocessableEntity, "invalid")
	loose := webStartG1(t, w, map[string]any{})
	if project, effort, limit := asstThreadRow(t, d, loose); project != nil || effort != "high" || limit != 200_000 {
		t.Errorf("new thread took %v %s %d, want the saved defaults", project, effort, limit)
	}

	created := w.do(http.MethodPost, "/web/v1/knowledge/spaces", map[string]any{"name": "Kitchen", "instructions": "Always answer in metric units.", "effort": "xhigh", "context_limit": 500000})
	webWantG1(t, created, http.StatusCreated)
	projectID := number(t, created.json["id"])
	id := strconv.FormatInt(projectID, 10)
	files := "/web/v1/knowledge/spaces/" + id + "/files"
	note := asstFile{"note.md", "text/markdown", []byte("# Note\nhello")}
	webWantG1(t, webFormG1(t, w, http.MethodPost, files, nil, []asstFile{note}), http.StatusCreated)
	webWantG1(t, webFormG1(t, w, http.MethodPost, files, nil, []asstFile{{"", "text/plain", []byte("nameless")}}), http.StatusUnprocessableEntity, "invalid")
	webWantG1(t, webFormG1(t, w, http.MethodPost, files, nil, []asstFile{{"empty.txt", "text/plain", nil}}), http.StatusUnprocessableEntity, "Choose at least one file.")
	var fileID int64
	if err := d.Read.QueryRow("SELECT id FROM thread_files WHERE project_id = ?", id).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	file := fmt.Sprintf("%s/%d", files, fileID)

	inside := webStartG1(t, w, map[string]any{"project_id": projectID})
	if project, effort, limit := asstThreadRow(t, d, inside); project == nil || *project != projectID || effort != "xhigh" || limit != 500_000 {
		t.Errorf("thread in the project took %v %s %d, want the project defaults", project, effort, limit)
	}
	webOKG1(t, w.do(http.MethodPut, threadG1(loose, "/project"), map[string]any{"project_id": projectID}))
	if project, _, _ := asstThreadRow(t, d, loose); project == nil {
		t.Fatal("the thread did not move into the project")
	}
	webOKG1(t, w.do(http.MethodPut, threadG1(loose, "/project"), map[string]any{"project_id": 0}))
	if project, _, _ := asstThreadRow(t, d, loose); project != nil {
		t.Fatal("the thread did not move out of the project")
	}

	webOKG1(t, w.do(http.MethodPost, "/web/v1/knowledge/spaces/"+id+"/archive", map[string]any{"archived": true}))
	fileCount := asstCount(t, d, "SELECT COUNT(*) FROM thread_files")
	threads := asstCount(t, d, "SELECT COUNT(*) FROM threads")
	expectAPI(t, webFormG1(t, w, http.MethodPost, files, nil, []asstFile{note}), http.StatusConflict, "project_archived")
	expectAPI(t, w.do(http.MethodDelete, file, nil), http.StatusConflict, "project_archived")
	expectAPI(t, webFormG1(t, w, http.MethodPost, "/web/v1/threads", map[string]string{"text": "hello", "project_id": id}, []asstFile{note}), http.StatusConflict, "project_archived")
	expectAPI(t, w.do(http.MethodPut, threadG1(loose, "/project"), map[string]any{"project_id": projectID}), http.StatusConflict, "project_archived")
	expectAPI(t, w.do(http.MethodPost, threadG1(inside, "/branch"), map[string]any{"seq": 1}), http.StatusConflict, "project_archived")
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files"); n != fileCount {
		t.Errorf("archived project files changed: %d, want %d", n, fileCount)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads {
		t.Errorf("archived project threads changed: %d, want %d", n, threads)
	}
	if project, _, _ := asstThreadRow(t, d, loose); project != nil {
		t.Error("a thread moved into the archived project")
	}
	if got := w.do(http.MethodGet, file+"/content", nil); got.status != http.StatusOK {
		t.Errorf("archived project file download %d", got.status)
	}

	webOKG1(t, w.do(http.MethodPost, "/web/v1/knowledge/spaces/"+id+"/archive", map[string]any{"archived": false}))
	webWantG1(t, webFormG1(t, w, http.MethodPost, files, nil, []asstFile{note}), http.StatusCreated)
	webOKG1(t, w.do(http.MethodPut, threadG1(loose, "/project"), map[string]any{"project_id": projectID}))
	webWantG1(t, w.do(http.MethodDelete, file, nil), http.StatusNoContent)
}

func TestAsstThreadModelIsPricedPerThreadAndADownSwitchDropsThinking(t *testing.T) {
	t.Parallel()
	_, api, d, w := webServeG1(t)
	api.Push(asstReply("sonnet here"), anthropictest.Reply("end_turn", anthropictest.Thinking("deep thought", "sig"), anthropictest.Text("opus here")))

	sent := len(api.Requests())
	thread := webStartG1(t, w, map[string]any{"text": "hi", "model": pricing.SonnetID, "effort": "low", "context_limit": 200000})
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

	webOKG1(t, w.do(http.MethodPatch, threadG1(thread, "/options"), map[string]any{"model": pricing.OpusID, "effort": "high", "context_limit": 500000}))
	webWantG1(t, w.do(http.MethodPost, threadG1(thread, "/messages"), map[string]any{"text": "again"}), http.StatusAccepted)
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
	webWantG1(t, w.do(http.MethodPost, "/web/v1/threads", map[string]any{"text": "hi", "model": "claude-fable-5-1"}), http.StatusUnprocessableEntity)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads"); n != threads {
		t.Errorf("a refused model created a thread")
	}
	webWantG1(t, w.do(http.MethodPatch, threadG1(thread, "/options"), map[string]any{"model": "gpt-5", "effort": "low", "context_limit": 90000}), http.StatusUnprocessableEntity)

	thinking := `SELECT COUNT(*) FROM thread_messages WHERE thread_id = ? AND role = 'assistant' AND content_json LIKE '%"type":"thinking"%'`
	if n := asstCount(t, d, thinking, thread); n != 1 {
		t.Fatalf("%d stored thinking blocks before the switch, want 1", n)
	}
	webOKG1(t, w.do(http.MethodPatch, threadG1(thread, "/options"), map[string]any{"model": pricing.SonnetID, "effort": "low", "context_limit": 200000}))
	if n := asstCount(t, d, "SELECT COUNT(*) FROM threads WHERE id = ? AND model = ?", thread, pricing.SonnetID); n != 1 {
		t.Fatal("the switch back to sonnet was not saved")
	}
	if n := asstCount(t, d, thinking, thread); n != 0 {
		t.Errorf("a down-switch kept %d thinking blocks", n)
	}
}

func TestAsstTodayOpensOneThreadUnderConcurrentVisits(t *testing.T) {
	t.Parallel()
	_, _, d, w := webServeG1(t)
	requests := make([]*http.Request, 8)
	for i := range requests {
		requests[i] = w.request(http.MethodPost, "/web/v1/today", nil)
	}
	threads := make([]int64, len(requests))
	var wg sync.WaitGroup
	for i, req := range requests {
		wg.Go(func() {
			resp, err := w.http.Do(req)
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var out struct {
				ThreadID int64 `json:"thread_id"`
			}
			if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&out) == nil {
				threads[i] = out.ThreadID
			}
		})
	}
	wg.Wait()
	for _, id := range threads {
		if id == 0 || id != threads[0] {
			t.Fatalf("today threads %v", threads)
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
	w := localWebG1(t, startServeIn(t, dir))
	webWantG1(t, w.do(http.MethodGet, "/web/v1/schedules?limit=100", nil), http.StatusOK, "Digest", "weekly-review")
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

	job := func(id string) string { return "/web/v1/schedules/" + id }
	webOKG1(t, w.do(http.MethodPut, job(digest)+"/enabled", map[string]any{"enabled": false}))
	if state(digest) == "active" {
		t.Error("disabling left the job active")
	}
	webOKG1(t, w.do(http.MethodPut, job(digest)+"/enabled", map[string]any{"enabled": true}))
	if state(digest) != "active" {
		t.Error("enabling did not enable the job")
	}

	weekly := jobID("weekly-review")
	webWantG1(t, w.do(http.MethodDelete, job(weekly), nil), http.StatusConflict)
	if state(weekly) == "cancelled" {
		t.Error("a built-in run was cancelled")
	}

	webWantG1(t, w.do(http.MethodDelete, job(digest), nil), http.StatusNoContent)
	if state(digest) != "cancelled" {
		t.Errorf("cancelled job state %s", state(digest))
	}
	if got := w.do(http.MethodPut, job(digest)+"/enabled", map[string]any{"enabled": false}); got.status != http.StatusNotFound {
		t.Errorf("disable of a cancelled job: %d, want 404", got.status)
	}
	schedule := func(id string) string { return column("schedule", id) }
	if got := w.do(http.MethodPatch, job(nightly), map[string]any{"schedule": "daily 03:15"}); got.status != http.StatusOK || schedule(nightly) != "daily 03:15" {
		t.Errorf("edit the nightly schedule = %d, stored %q: %.200s", got.status, schedule(nightly), got.body)
	}
	if got := w.do(http.MethodPatch, job(nightly), map[string]any{"schedule": "whenever"}); got.status != http.StatusUnprocessableEntity || schedule(nightly) != "daily 03:15" {
		t.Errorf("an invalid schedule = %d, stored %q, want a refusal that keeps the old one", got.status, schedule(nightly))
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
	if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation LIKE 'assistant.schedule.%' AND source = 'api' AND actor = 'operator'"); n != 7 {
		t.Errorf("operator schedule audits = %d, want 7", n)
	}
}

func TestBranchKeepsRunOptions(t *testing.T) {
	t.Parallel()
	_, api, d, w := webServeG1(t)
	api.Push(asstReply("branch source answer"))
	thread := webStartG1(t, w, map[string]any{"text": "branch options", "web_search": false, "cost_cap_usd": 2.5})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("source run %s", state)
	}
	var seq int64
	if err := d.Read.QueryRow("SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	made := w.do(http.MethodPost, threadG1(thread, "/branch"), map[string]any{"seq": seq})
	webWantG1(t, made, http.StatusCreated)
	child := number(t, made.json["thread_id"])
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

func TestSpaceFilesStayInTheirSpaceAndAreAudited(t *testing.T) {
	t.Parallel()
	_, _, d, w := webServeG1(t)
	project := func(name string) string {
		created := w.do(http.MethodPost, "/web/v1/knowledge/spaces", map[string]any{"name": name, "effort": "high", "context_limit": 200000})
		webWantG1(t, created, http.StatusCreated)
		return strconv.FormatInt(number(t, created.json["id"]), 10)
	}
	home, other := project("Home"), project("Other")
	files := func(space string) string { return "/web/v1/knowledge/spaces/" + space + "/files" }
	webWantG1(t, webFormG1(t, w, http.MethodPost, files(home), nil, []asstFile{{"plan.txt", "text/plain", []byte("the plan")}}), http.StatusCreated)
	var fileID int64
	var stored string
	if err := d.Read.QueryRow("SELECT id, path FROM thread_files WHERE project_id = ?", home).Scan(&fileID, &stored); err != nil {
		t.Fatal(err)
	}
	if got := w.do(http.MethodGet, fmt.Sprintf("%s/%d/content", files(home), fileID), nil); got.status != http.StatusOK || got.body != "the plan" {
		t.Fatalf("own project download: %d %q", got.status, got.body)
	}
	for _, path := range []string{
		fmt.Sprintf("%s/%d/content", files(other), fileID),
		fmt.Sprintf("%s/%d/content", files(home), fileID+1000),
		threadG1(1, fmt.Sprintf("/files/%d/content", fileID)),
	} {
		if got := w.do(http.MethodGet, path, nil); got.status != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, got.status)
		}
	}
	if got := w.do(http.MethodDelete, fmt.Sprintf("%s/%d", files(other), fileID), nil); got.status != http.StatusNotFound {
		t.Fatalf("delete through another project: %d, want 404", got.status)
	}
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files WHERE id = ?", fileID); n != 1 {
		t.Fatalf("a foreign delete removed the file row")
	}
	webWantG1(t, w.do(http.MethodDelete, fmt.Sprintf("%s/%d", files(home), fileID), nil), http.StatusNoContent)
	if n := asstCount(t, d, "SELECT COUNT(*) FROM thread_files WHERE id = ?", fileID); n != 0 {
		t.Errorf("the deleted file row remains")
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Errorf("the deleted file remains on disk: %v", err)
	}
	for _, want := range []struct{ operation, outcome, project string }{
		{"knowledge.space.file.upload", "ok", home},
		{"knowledge.space.file.delete", "not_found", other},
		{"knowledge.space.file.delete", "ok", home},
	} {
		if n := asstCount(t, d, "SELECT COUNT(*) FROM operation_audit WHERE operation = ? AND outcome = ? AND target = ?", want.operation, want.outcome, "project:"+want.project); n != 1 {
			t.Errorf("%s %s audits for project %s: %d, want 1", want.operation, want.outcome, want.project, n)
		}
	}
}
