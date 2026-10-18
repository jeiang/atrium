// Package anthropictest is a fake Anthropic API (tests/fake_anthropic.py): an httptest server that answers
// Messages requests from a script, as SSE when the request streams, and a recording transport that keeps
// every request the client sent.
package anthropictest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/config"
)

const APIKey = "fake-anthropic-key"

// Response is one scripted reply in the shape of the goldens' `response`. With Status set the server
// answers that HTTP status with ErrorBody; with ErrorBody alone a stream sends it as an SSE error event
// right after message_start.
type Response struct {
	Content           []json.RawMessage `json:"content"`
	StopReason        string            `json:"stop_reason"`
	Usage             json.RawMessage   `json:"usage"`
	ContextManagement json.RawMessage   `json:"context_management"`
	Status            int               `json:"-"`
	ErrorBody         json.RawMessage   `json:"-"`
}

type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

func (r Request) JSON(t testing.TB) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("anthropictest: request body: %v", err)
	}
	return v
}

type Server struct {
	*httptest.Server
	t testing.TB
	// OnRequest runs in the handler before a Messages reply; it may block to hold a call in flight.
	OnRequest func(Request)
	// FileDeleteStatus, when set, fails every Files API delete with that status.
	FileDeleteStatus int

	mu       sync.Mutex
	script   []Response
	requests []Request
	served   int
}

func New(t testing.TB, script ...Response) *Server {
	t.Helper()
	s := &Server{t: t, script: script}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Push(script ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, script...)
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Remaining is the number of scripted responses not yet served.
func (s *Server) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.script)
}

func (s *Server) Env() config.Env {
	return config.Map(map[string]string{"ANTHROPIC_API_KEY": APIKey, anthropic.BaseURLEnv: s.URL})
}

// Client is an adapter client for this server whose transport records each request it sends.
func (s *Server) Client() *anthropic.Client {
	c, err := anthropic.New(s.Env(), anthropic.HTTPClient(s.HTTPClient()))
	if err != nil {
		s.t.Fatalf("anthropictest: %v", err)
	}
	return c
}

func (s *Server) HTTPClient() *http.Client {
	return &http.Client{Transport: &recorder{s: s, next: s.Server.Client().Transport}}
}

type recorder struct {
	s    *Server
	next http.RoundTripper
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	r.s.mu.Lock()
	r.s.requests = append(r.s.requests, Request{req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), body})
	r.s.mu.Unlock()
	return r.next.RoundTrip(req)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		s.messages(w, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/files/"):
		s.deleteFile(w, strings.TrimPrefix(r.URL.Path, "/v1/files/"))
	default:
		fail(w, http.StatusNotFound, ErrorBody("not_found_error", "no such endpoint"))
	}
}

func (s *Server) deleteFile(w http.ResponseWriter, id string) {
	if s.FileDeleteStatus != 0 {
		fail(w, s.FileDeleteStatus, ErrorBody("api_error", "delete failed"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "type": "file_deleted"})
}

func (s *Server) messages(w http.ResponseWriter, req Request) {
	if s.OnRequest != nil {
		s.OnRequest(req)
	}
	var head struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(req.Body, &head)
	s.mu.Lock()
	if len(s.script) == 0 {
		s.mu.Unlock()
		s.t.Errorf("anthropictest: no scripted response left")
		fail(w, http.StatusInternalServerError, ErrorBody("api_error", "no scripted response left"))
		return
	}
	resp := s.script[0]
	s.script = s.script[1:]
	s.served++
	id := fmt.Sprintf("msg_fake_%02d", s.served)
	s.mu.Unlock()
	switch {
	case resp.Status != 0:
		fail(w, resp.Status, resp.ErrorBody)
	case head.Stream:
		stream(w, id, head.Model, resp)
	case resp.ErrorBody != nil:
		fail(w, http.StatusInternalServerError, resp.ErrorBody)
	default:
		writeJSON(w, message(id, head.Model, resp, resp.Content))
	}
}

func fail(w http.ResponseWriter, status int, body json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Should-Retry", "false")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func orNull(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage("null")
	}
	return v
}

func message(id, model string, resp Response, content []json.RawMessage) map[string]any {
	if content == nil {
		content = []json.RawMessage{}
	}
	var stop any
	if resp.StopReason != "" {
		stop = resp.StopReason
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model, "content": content,
		"stop_reason": stop, "stop_sequence": nil, "usage": orNull(resp.Usage), "context_management": orNull(resp.ContextManagement),
	}
}

type sse struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s sse) send(event string, data any) {
	payload, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, payload)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// stream sends a reply the way the API streams it: each block starts empty where the API streams its
// fields (text, thinking and signature, tool input, compaction content) and the deltas fill it in.
func stream(w http.ResponseWriter, id, model string, resp Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	out := sse{w, flusher}
	start := message(id, model, resp, nil)
	delete(start, "context_management")
	start["stop_reason"] = nil
	out.send("message_start", map[string]any{"type": "message_start", "message": start})
	out.send("ping", map[string]string{"type": "ping"})
	if resp.ErrorBody != nil {
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", resp.ErrorBody)
		return
	}
	for i, raw := range resp.Content {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			panic(fmt.Sprintf("anthropictest: content block %d: %v", i, err))
		}
		first, deltas := split(block)
		out.send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": first})
		for _, d := range deltas {
			out.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": d})
		}
		out.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	delta := map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp.StopReason, "stop_sequence": nil},
		"usage": orNull(resp.Usage),
	}
	if cm := orNull(resp.ContextManagement); string(cm) != "null" {
		delta["context_management"] = cm
	}
	out.send("message_delta", delta)
	out.send("message_stop", map[string]string{"type": "message_stop"})
}

func str(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

func split(block map[string]json.RawMessage) (map[string]json.RawMessage, []map[string]any) {
	first := make(map[string]json.RawMessage, len(block))
	for k, v := range block {
		first[k] = v
	}
	var deltas []map[string]any
	switch str(block["type"]) {
	case "text":
		first["text"] = json.RawMessage(`""`)
		delete(first, "citations")
		var citations []json.RawMessage
		_ = json.Unmarshal(block["citations"], &citations)
		for _, c := range citations {
			deltas = append(deltas, map[string]any{"type": "citations_delta", "citation": c})
		}
		if t := str(block["text"]); t != "" {
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": t})
		}
	case "thinking":
		first["thinking"], first["signature"] = json.RawMessage(`""`), json.RawMessage(`""`)
		if t := str(block["thinking"]); t != "" {
			deltas = append(deltas, map[string]any{"type": "thinking_delta", "thinking": t})
		}
		if sig := str(block["signature"]); sig != "" {
			deltas = append(deltas, map[string]any{"type": "signature_delta", "signature": sig})
		}
	case "tool_use", "server_tool_use":
		first["input"] = json.RawMessage(`{}`)
		var input map[string]json.RawMessage
		if json.Unmarshal(block["input"], &input) == nil && len(input) > 0 {
			deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": string(block["input"])})
		}
	case "compaction":
		delete(first, "content")
		delete(first, "encrypted_content")
		deltas = append(deltas, map[string]any{"type": "compaction_delta", "content": block["content"], "encrypted_content": block["encrypted_content"]})
	}
	return first, deltas
}
