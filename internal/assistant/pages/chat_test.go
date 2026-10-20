package pages

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/py"
)

var pngBytes = func() []byte {
	b, err := py.B64Decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return b
}()

func (s *site) startThread(text string, uploads ...upload) int64 {
	s.t.Helper()
	r := s.multipart("/chat", map[string]string{"text": text}, uploads, hx, "true")
	status(s.t, r, http.StatusNoContent)
	to := r.Header.Get("HX-Redirect")
	id, err := strconv.ParseInt(strings.TrimPrefix(to, "/chat/"), 10, 64)
	if err != nil {
		s.t.Fatalf("HX-Redirect %q", to)
	}
	return id
}

func (s *site) hold() (release func()) {
	gate := make(chan struct{})
	s.api.OnRequest = func(anthropictest.Request) { <-gate }
	return func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}
}

func (s *site) waitRequests(n int) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(s.api.Requests()) < n {
		if time.Now().After(deadline) {
			s.t.Fatalf("%d model requests, want %d", len(s.api.Requests()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func frameIDs(body string) []string {
	var ids []string
	for line := range strings.SplitSeq(body, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestRemovedPagesAndResponseMatrix(t *testing.T) {
	s := newSite(t)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/web", 404}, {http.MethodGet, "/contacts", 404}, {http.MethodGet, "/web?q=x", 404},
		{http.MethodPost, "/ask", 404}, {http.MethodPost, "/chat/1/sandbox", 404}, {http.MethodGet, "/chat/999", 404},
		{http.MethodGet, "/chat/999/work", 404}, {http.MethodPost, "/chat/999/regenerate", 404}, {http.MethodPost, "/chat/999/delete", 404},
		{http.MethodPost, "/chat/999/pin", 404}, {http.MethodPost, "/chat/999/archive", 404}, {http.MethodPost, "/chat/999/project", 404},
		{http.MethodPost, "/chat/999/options", 422}, {http.MethodPost, "/chat/999/messages?x=1", 422},
		{http.MethodGet, "/chat/x", 422}, {http.MethodGet, "/chat/1/events", 422}, {http.MethodGet, "/chat/1/events?run=9", 404},
		{http.MethodGet, "/chat/1/runs/9", 404}, {http.MethodPost, "/chat/1/runs/9/resume", 404}, {http.MethodPost, "/chat/1/stop", 409},
		{http.MethodPost, "/chat/1/branch", 422}, {http.MethodGet, "/chat/1/files/1", 404}, {http.MethodGet, "/projects/9/files", 404},
		{http.MethodGet, "/projects/9/files/1/download", 404},
	} {
		var r *reply
		if c.method == http.MethodGet {
			r = s.get(c.path)
		} else {
			r = s.post(c.path, nil)
		}
		if r.StatusCode != c.want {
			t.Errorf("%s %s = %d, want %d: %.200s", c.method, c.path, r.StatusCode, c.want, r.body)
		}
	}
	if r := s.post("/chat/1/stop", nil); detail(t, r.body) != "no run is running" {
		t.Errorf("stop detail %s", r.body)
	}
	if r := s.get("/"); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/chat" {
		t.Errorf("home %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := s.get("/?source=notion"); r.Header.Get("Location") != "/chat" {
		t.Errorf("home with query %s", r.Header.Get("Location"))
	}
	page := status(t, s.get("/chat"), http.StatusOK)
	mustContain(t, page, "No threads yet", "Scheduled runs live under Scheduled.", `aria-current="page"`)
	mustNotContain(t, page, `href="/web"`, `href="/contacts"`, `href="/agents"`)
	status(t, s.get("/chat?view=bogus"), http.StatusUnprocessableEntity)
	toast := status(t, s.get("/chat?view=bogus", hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, toast, "Filter not understood", "Unknown view.")
}

func TestChatTurnStreamsAndSettles(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Thinking("pondering", "sig"), anthropictest.Text("Hello **there**")))
	thread := s.startThread("hi")
	run := s.lastRun(thread)
	if got := s.settle(run); got.State != journal.Done {
		t.Fatalf("run %s %v", got.State, got.Error)
	}
	events := s.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run))
	body := status(t, events, http.StatusOK)
	for header, want := range map[string]string{
		"Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache", "X-Accel-Buffering": "no", "X-Content-Type-Options": "nosniff",
	} {
		if got := events.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	mustContain(t, body, "event: thinking\ndata: {\"run_id\": "+strconv.FormatInt(run, 10)+", \"text\": \"pondering\"}", "event: text\n", "event: run_done\n")
	if !strings.HasSuffix(body, "event: done\ndata: {\"state\": \"done\"}\n\n") {
		t.Errorf("stream end %q", body[max(0, len(body)-80):])
	}
	ids := frameIDs(body)
	if len(ids) < 2 {
		t.Fatalf("frames %q", body)
	}
	last := ids[len(ids)-1]
	resumed := status(t, s.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run), "Last-Event-ID", ids[len(ids)-2]), http.StatusOK)
	if got := frameIDs(resumed); len(got) != 1 || got[0] != last {
		t.Errorf("resumed frames %v", got)
	}
	done := status(t, s.get(fmt.Sprintf("/chat/%d/runs/%d?after=0", thread, run), hx, "true"), http.StatusOK)
	mustContain(t, done, "<strong>there</strong>", `id="meter"`, `hx-swap-oob="true"`, `data-seq="1"`)
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, "<strong>there</strong>", "1 step", `data-action="edit-message"`, "/regenerate", `aria-label="Conversation"`)
	list := status(t, s.get("/chat?q=hi", hx, "true"), http.StatusOK)
	mustContain(t, list, fmt.Sprintf(`href="/chat/%d"`, thread), "Recent")
	mustContain(t, status(t, s.get("/chat?q=zzzz", hx, "true"), http.StatusOK), "Nothing matches", "No thread mentions “zzzz”.")
}

func TestLiveRunQueueStopAndKeepalive(t *testing.T) {
	s := newSite(t)
	release := s.hold()
	defer release()
	s.api.Push(reply1(anthropictest.Text("first")), reply1(anthropictest.Text("second")))
	thread := s.startThread("long job")
	run := s.lastRun(thread)
	s.waitRequests(1)
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, fmt.Sprintf(`data-live-run="%d"`, run), "thinking…", `<span data-queue>Queue</span>`)
	mustNotContain(t, page, `data-action="edit-message"`)
	live := status(t, s.get(fmt.Sprintf("/chat/%d/runs/%d?after=1", thread, run), hx, "true"), http.StatusOK)
	mustContain(t, live, `data-after="1"`, `hx-trigger="run-done"`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/chat/%d/events?run=%d", s.http.URL, thread, run), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stream := bufio.NewReader(resp.Body)
	keepalive := false
	for !keepalive {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a keepalive: %v", err)
		}
		keepalive = line == ": keepalive\n"
	}
	_ = resp.Body.Close()
	queued := status(t, s.multipart(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "and this"}, nil, hx, "true"), http.StatusOK)
	mustContain(t, queued, "data-queued=", "and this", "Queued: joins the conversation at the assistant's next step")
	busy := status(t, s.multipart(fmt.Sprintf("/chat/%d/regenerate", thread), nil, nil, hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, busy, "Still working")
	status(t, s.post(fmt.Sprintf("/chat/%d/stop", thread), nil), http.StatusNoContent)
	release()
	got := s.settle(run)
	if got.State != journal.Failed || got.Error == nil || !strings.Contains(*got.Error, "stop") {
		t.Errorf("stopped run %s %v", got.State, got.Error)
	}
	if r := s.post(fmt.Sprintf("/chat/%d/stop", thread), nil); r.StatusCode != http.StatusConflict {
		t.Errorf("second stop %d", r.StatusCode)
	}
	done := status(t, s.get(fmt.Sprintf("/chat/%d/runs/%d?after=0", thread, run), hx, "true"), http.StatusOK)
	mustContain(t, done, fmt.Sprintf("Run %d stopped:", run))
	mustNotContain(t, done, "Try again")
}

func TestStreamsEndOnShutdown(t *testing.T) {
	s := newSite(t)
	release := s.hold()
	defer release()
	s.api.Push(reply1(anthropictest.Text("late")))
	thread := s.startThread("hold on")
	run := s.lastRun(thread)
	s.waitRequests(1)
	resp, err := http.Get(fmt.Sprintf("%s/chat/%d/events?run=%d", s.http.URL, thread, run))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		ended <- err
	}()
	time.Sleep(50 * time.Millisecond)
	s.srv.StopStreams()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream outlived the shutdown signal")
	}
	release()
}

func TestShutdownWaitsForRunToFinish(t *testing.T) {
	s := newSite(t)
	release := s.hold()
	defer release()
	s.api.Push(reply1(anthropictest.Text("finished")))
	thread := s.startThread("long job")
	run := s.lastRun(thread)
	s.waitRequests(1)
	s.srv.StopStreams()
	closed := make(chan struct{})
	go func() {
		s.srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("shutdown returned while a run was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown never returned after the run was released")
	}
	got, err := journal.GetRun(context.Background(), s.db.Read, run)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.State != journal.Done {
		t.Errorf("run at shutdown ended %+v, want done", got)
	}
}

func TestUploadsLimitsAndFileServing(t *testing.T) {
	s := newSite(t)
	huge := upload{"huge.bin", "application/octet-stream", make([]byte, files.MessageCapBytes+(2<<20))}
	refused := status(t, s.multipartVia(s.chunked, "/chat", map[string]string{"text": "take this"}, []upload{huge}, hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, refused, "Not attached", "files in one message must total under 64 MB")
	if n := s.count("SELECT COUNT(*) FROM threads") + s.count("SELECT COUNT(*) FROM thread_files"); n != 0 {
		t.Errorf("an oversize body left %d rows", n)
	}
	big := upload{"big.txt", "text/plain", make([]byte, files.FileCapBytes+1)}
	refused = status(t, s.multipart("/chat", map[string]string{"text": "take this"}, []upload{big}, hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, refused, "Not attached", "big.txt is over the 32 MB limit")
	if n := s.count("SELECT COUNT(*) FROM thread_files"); n != 0 {
		t.Errorf("a refused file left %d rows", n)
	}
	small := upload{"a.txt", "text/plain", []byte("hi")}
	mustContain(t, status(t, s.multipart("/chat", nil, []upload{small}, hx, "true"), http.StatusUnprocessableEntity), "Nothing sent", "Type a message to go with the files.")
	status(t, s.multipart("/chat", nil, []upload{small}), http.StatusUnprocessableEntity)

	s.api.Push(reply1(anthropictest.Text("got them")))
	html := upload{`report "q" 1.html`, "text/html", []byte("<script>alert(1)</script>")}
	csvFile := upload{"data.csv", "text/csv", []byte("a,\"b,c\"\n\nx,\xff\n")}
	thread := s.startThread("look", html, upload{"pic.png", "image/png", pngBytes}, csvFile)
	s.settle(s.lastRun(thread))
	ids := map[string]int64{}
	q, err := s.db.Read.Query("SELECT id, name FROM thread_files WHERE thread_id = ?", thread)
	if err != nil {
		t.Fatal(err)
	}
	for q.Next() {
		var id int64
		var name string
		_ = q.Scan(&id, &name)
		ids[name] = id
	}
	_ = q.Close()
	if len(ids) != 3 {
		t.Fatalf("stored %v", ids)
	}
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, `aria-label="Attached files"`, fmt.Sprintf(`hx-get="/chat/%d/files/%d"`, thread, ids["pic.png"]), "data-files")

	base := fmt.Sprintf("/chat/%d/files/%d", thread, ids[html.name])
	dl := s.get(base + "/download")
	if dl.StatusCode != http.StatusOK || dl.body != "<script>alert(1)</script>" {
		t.Fatalf("download %d %q", dl.StatusCode, dl.body)
	}
	for header, want := range map[string]string{
		"Content-Type": "text/html; charset=utf-8", "X-Content-Type-Options": "nosniff",
		"Content-Disposition": "attachment; filename*=utf-8''report%20%22q%22%201.html",
	} {
		if got := dl.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	plain := s.get(fmt.Sprintf("/chat/%d/files/%d/download", thread, ids["data.csv"]))
	if got := plain.Header.Get("Content-Disposition"); got != `attachment; filename="data.csv"` {
		t.Errorf("plain disposition %q", got)
	}
	status(t, s.get(base+"/raw"), http.StatusNotFound)
	raw := s.get(fmt.Sprintf("/chat/%d/files/%d/raw", thread, ids["pic.png"]))
	if raw.StatusCode != http.StatusOK || raw.Header.Get("Content-Type") != "image/png" || raw.Header.Get("Content-Disposition") != "" {
		t.Errorf("raw %d %v", raw.StatusCode, raw.Header)
	}
	image := status(t, s.get(fmt.Sprintf("/chat/%d/files/%d", thread, ids["pic.png"]), hx, "true"), http.StatusOK)
	mustContain(t, image, fmt.Sprintf(`src="/chat/%d/files/%d/raw"`, thread, ids["pic.png"]))
	table := status(t, s.get(fmt.Sprintf("/chat/%d/files/%d", thread, ids["data.csv"]), hx, "true"), http.StatusOK)
	mustContain(t, table, `<th class="border px-2 py-1 font-medium">b,c</th>`, "<tr></tr>", ">x</td>", "\ufffd</td>")
	other := s.startThread("")
	status(t, s.get(fmt.Sprintf("/chat/%d/files/%d", other, ids["pic.png"])), http.StatusNotFound)
	status(t, s.get(fmt.Sprintf("/chat/%d/files/%d/download", other, ids["pic.png"])), http.StatusNotFound)
}

func TestForeignAttachmentIsUnavailable(t *testing.T) {
	s := newSite(t)
	owner := s.startThread("")
	blocks, err := files.IngestUploads(context.Background(), s.db.Write, s.cfg.FilesDir(),
		[]files.UploadedFile{{Name: "secret.txt", Data: []byte("TOPSECRET"), Type: "text/plain"}}, 1, files.Owner{ThreadID: owner}, nil)
	if err != nil {
		t.Fatal(err)
	}
	thief := s.startThread("")
	foreign, _ := json.Marshal(blocks[0])
	if _, err := journal.AppendMessage(context.Background(), s.db, thief, nil, journal.User, []json.RawMessage{foreign, json.RawMessage(`{"type": "text", "text": "earlier"}`)}, 2, nil); err != nil {
		t.Fatal(err)
	}
	s.api.Push(reply1(anthropictest.Text("ok")))
	status(t, s.multipart(fmt.Sprintf("/chat/%d/messages", thief), map[string]string{"text": "read it"}, nil, hx, "true"), http.StatusOK)
	s.settle(s.lastRun(thief))
	body := string(s.api.Requests()[0].Body)
	mustContain(t, body, "[attachment secret.txt is no longer available]")
	mustNotContain(t, body, "TOPSECRET")
}

func TestEditRegenerateBranch(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text("Answer one")))
	thread := s.startThread("first")
	s.settle(s.lastRun(thread))
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	if n := strings.Count(page, `data-action="edit-message"`); n != 1 {
		t.Errorf("%d edit buttons", n)
	}
	s.api.Push(reply1(anthropictest.Text("Answer two")))
	edited := s.multipart(fmt.Sprintf("/chat/%d/messages", thread), map[string]string{"text": "second", "edit": "1"}, nil, hx, "true")
	mustContain(t, status(t, edited, http.StatusOK), "second", "data-live-run")
	if edited.Header.Get("HX-Reswap") != "innerHTML" {
		t.Errorf("HX-Reswap %q", edited.Header.Get("HX-Reswap"))
	}
	s.settle(s.lastRun(thread))
	page = status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, "Answer two")
	mustNotContain(t, page, "Answer one", "data-text>first<")
	s.api.Push(reply1(anthropictest.Text("Answer three")))
	mustContain(t, status(t, s.post(fmt.Sprintf("/chat/%d/regenerate", thread), nil, hx, "true"), http.StatusOK), "data-live-run")
	s.settle(s.lastRun(thread))
	mustContain(t, status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK), "Answer three")
	var seq int64
	if err := s.db.Read.QueryRow("SELECT MIN(seq) FROM thread_messages WHERE thread_id = ? AND superseded = 0", thread).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	moved := s.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {strconv.FormatInt(seq, 10)}})
	status(t, moved, http.StatusSeeOther)
	child := moved.Header.Get("Location")
	mustContain(t, status(t, s.get(child), http.StatusOK), "Branched from “first”", fmt.Sprintf(`title="Branched from message %d"`, seq))
	status(t, s.post(fmt.Sprintf("/chat/%d/branch", thread), url.Values{"seq": {"99"}}), http.StatusNotFound)
	empty := s.startThread("")
	mustContain(t, status(t, s.post(fmt.Sprintf("/chat/%d/regenerate", empty), nil, hx, "true"), http.StatusUnprocessableEntity), "Nothing to redo")
	mustContain(t, status(t, s.multipart(fmt.Sprintf("/chat/%d/messages", empty), map[string]string{"text": "  "}, nil, hx, "true"), http.StatusUnprocessableEntity), "Nothing sent", "Type a message first.")
}

func TestListFlagsOptionsProjectsDelete(t *testing.T) {
	s := newSite(t)
	s.exec("INSERT INTO projects (name, instructions, effort, context_limit, archived, created_at, updated_at) VALUES ('Home', '', 'high', 90000, 0, 1, 1)")
	thread := s.startThread("")
	in := s.multipart("/chat", map[string]string{"text": "", "project_id": "1"}, nil, hx, "true")
	status(t, in, http.StatusNoContent)
	status(t, s.multipart("/chat", map[string]string{"project_id": "77"}, nil), http.StatusNotFound)
	list := status(t, s.post(fmt.Sprintf("/chat/%d/pin", thread), url.Values{"pinned": {"1"}, "q": {""}, "view": {""}}, hx, "true"), http.StatusOK)
	mustContain(t, list, `aria-label="Pinned"`, `"pinned": 0, "project_id": 0`, "Home")
	if r := s.post(fmt.Sprintf("/chat/%d/archive", thread), url.Values{"archived": {"1"}}); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/chat" {
		t.Errorf("archive %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	mustContain(t, status(t, s.get("/chat?view=archived"), http.StatusOK), `aria-label="Archived"`, `name="view" value="archived"`)
	status(t, s.post(fmt.Sprintf("/chat/%d/pin", thread), url.Values{"pinned": {"x"}}), http.StatusUnprocessableEntity)
	meter := status(t, s.post(fmt.Sprintf("/chat/%d/options", thread), url.Values{"effort": {"low"}, "context_limit": {"500000"}}, hx, "true"), http.StatusOK)
	mustContain(t, meter, `id="meter"`, "effort low", "0.0K / ")
	mustContain(t, status(t, s.post(fmt.Sprintf("/chat/%d/options", thread), url.Values{"effort": {"huge"}}, hx, "true"), http.StatusUnprocessableEntity), "Not saved")
	page := status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK)
	mustContain(t, page, `<option value="500000" selected>500K (5× price over 100K)</option>`, `<option value="low" selected>low</option>`, `id="move-project"`)
	if r := s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {"1"}}); r.StatusCode != http.StatusSeeOther {
		t.Errorf("move %d", r.StatusCode)
	}
	mustContain(t, status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusOK), `href="/projects/1"`, `<option value="1" selected>Home</option>`)
	status(t, s.post(fmt.Sprintf("/chat/%d/project", thread), url.Values{"project_id": {"5"}}), http.StatusNotFound)
	deleted := s.post(fmt.Sprintf("/chat/%d/delete", thread), nil, hx, "true")
	if deleted.StatusCode != http.StatusNoContent || deleted.Header.Get("HX-Redirect") != "/chat" {
		t.Errorf("delete %d %v", deleted.StatusCode, deleted.Header)
	}
	status(t, s.get(fmt.Sprintf("/chat/%d", thread)), http.StatusNotFound)

	pfiles := status(t, s.multipart("/projects/1/files", nil, []upload{{"brief.md", "text/markdown", []byte("# Brief\nhello")}}, hx, "true"), http.StatusOK)
	mustContain(t, pfiles, "brief.md", "~0.0K tokens", `hx-post="/projects/1/files/`)
	mustContain(t, status(t, s.multipart("/projects/1/files", nil, nil, hx, "true"), http.StatusUnprocessableEntity), "Choose at least one file.")
	var fid int64
	if err := s.db.Read.QueryRow("SELECT id FROM thread_files WHERE project_id = 1").Scan(&fid); err != nil {
		t.Fatal(err)
	}
	dl := s.get(fmt.Sprintf("/projects/1/files/%d/download", fid))
	if dl.StatusCode != http.StatusOK || dl.Header.Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Errorf("project download %d %v", dl.StatusCode, dl.Header)
	}
	status(t, s.get(fmt.Sprintf("/projects/2/files/%d/download", fid)), http.StatusNotFound)
	mustContain(t, status(t, s.post(fmt.Sprintf("/projects/1/files/%d/delete", fid), nil, hx, "true"), http.StatusOK), "Files every chat in this project can read")
}

func TestEventsMalformedLastEventID(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text("ok")))
	thread := s.startThread("hi")
	run := s.lastRun(thread)
	s.settle(run)
	r := s.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run), "Last-Event-ID", "not-a-number")
	status(t, r, http.StatusBadRequest)
}

func TestStreamDrainsRunDoneWrittenAfterTerminalState(t *testing.T) {
	s := newSite(t)
	release := s.hold()
	defer release()
	s.api.Push(reply1(anthropictest.Text("late")))
	thread := s.startThread("race")
	run := s.lastRun(thread)
	s.waitRequests(1)
	s.exec("UPDATE runs SET state = ? WHERE id = ?", journal.Done, run)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = s.db.Write.Exec("INSERT INTO run_events (run_id, ts, kind, data_json) VALUES (?, ?, ?, ?)", run, 1.0, "run_done", `{"state": "done"}`)
	}()
	body := status(t, s.get(fmt.Sprintf("/chat/%d/events?run=%d", thread, run)), http.StatusOK)
	mustContain(t, body, "event: run_done\n", "event: done\ndata: {\"state\": \"done\"}\n\n")
	release()
}

func TestRawImageMissingFromStorage(t *testing.T) {
	s := newSite(t)
	s.api.Push(reply1(anthropictest.Text("got it")))
	thread := s.startThread("look", upload{"pic.png", "image/png", pngBytes})
	s.settle(s.lastRun(thread))
	var id int64
	var path string
	if err := s.db.Read.QueryRow("SELECT id, path FROM thread_files WHERE thread_id = ?", thread).Scan(&id, &path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	status(t, s.get(fmt.Sprintf("/chat/%d/files/%d/raw", thread, id)), http.StatusNotFound)
}
