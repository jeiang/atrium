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
	"sync/atomic"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/config"
)

const APIKey = "fake-anthropic-key"

var fakeIDs atomic.Int64

type Response struct {
	Content           []json.RawMessage `json:"content"`
	StopReason        string            `json:"stop_reason"`
	StopDetails       json.RawMessage   `json:"stop_details"`
	Usage             json.RawMessage   `json:"usage"`
	ContextManagement json.RawMessage   `json:"context_management"`
	Status            int               `json:"-"`
	ErrorBody         json.RawMessage   `json:"-"`
	Header            http.Header       `json:"-"`

	StartUsage          json.RawMessage `json:"-"`
	InterruptAfterStart bool            `json:"-"`
	Stall               bool            `json:"-"`
	ID                  string          `json:"-"`
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
	t                testing.TB
	OnRequest        func(Request)
	RejectKey        string
	FileDeleteStatus int
	Models           []string
	Disconnect       atomic.Bool
	modelLists       atomic.Int64

	mu         sync.Mutex
	script     []Response
	jsonScript []Response
	requests   []Request
	served     int
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

func (s *Server) ModelLists() int64 {
	return s.modelLists.Load()
}

func (s *Server) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.script)
}

func (s *Server) PushJSON(script ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jsonScript = append(s.jsonScript, script...)
}

func (s *Server) JSONRemaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jsonScript)
}
func (s *Server) Env() config.Env {
	return config.Map(map[string]string{"ANTHROPIC_API_KEY": APIKey, anthropic.BaseURLEnv: s.URL})
}

func (s *Server) Client() *anthropic.Client {
	c, err := anthropic.New(APIKey, s.Env(), anthropic.HTTPClient(s.HTTPClient()))
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
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		s.modelLists.Add(1)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		s.messages(w, r, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models" && s.RejectKey != "" && r.Header.Get("X-Api-Key") == s.RejectKey:
		fail(w, http.StatusUnauthorized, ErrorBody("authentication_error", "invalid x-api-key"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		s.models(w)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/files/"):
		s.deleteFile(w, strings.TrimPrefix(r.URL.Path, "/v1/files/"))
	default:
		fail(w, http.StatusNotFound, ErrorBody("not_found_error", "no such endpoint"))
	}
}

func (s *Server) models(w http.ResponseWriter) {
	data := make([]map[string]string, len(s.Models))
	for i, id := range s.Models {
		data[i] = map[string]string{"type": "model", "id": id, "display_name": id, "created_at": "2026-01-01T00:00:00Z"}
	}
	writeJSON(w, map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil})
}

func (s *Server) deleteFile(w http.ResponseWriter, id string) {
	if s.FileDeleteStatus != 0 {
		fail(w, s.FileDeleteStatus, ErrorBody("api_error", "delete failed"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "type": "file_deleted"})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, req Request) {
	if s.OnRequest != nil {
		s.OnRequest(req)
	}
	if s.Disconnect.Load() {
		s.hijackClose(w)
		return
	}
	var head struct {
		Model        string `json:"model"`
		Stream       bool   `json:"stream"`
		OutputConfig struct {
			Format json.RawMessage `json:"format"`
		} `json:"output_config"`
	}
	_ = json.Unmarshal(req.Body, &head)
	queue, name := &s.script, "scripted"
	if len(head.OutputConfig.Format) > 0 {
		if err := fixedJSONRequest(req.Body); err != nil {
			s.t.Errorf("anthropictest: fixed JSON request: %v", err)
			fail(w, http.StatusBadRequest, ErrorBody("invalid_request_error", err.Error()))
			return
		}
		queue, name = &s.jsonScript, "scripted fixed JSON"
	}
	s.mu.Lock()
	if len(*queue) == 0 {
		s.mu.Unlock()
		s.t.Errorf("anthropictest: no %s response left", name)
		fail(w, http.StatusInternalServerError, ErrorBody("api_error", "no "+name+" response left"))
		return
	}
	resp := (*queue)[0]
	*queue = (*queue)[1:]
	s.served++
	id := resp.ID
	if id == "" {
		id = fmt.Sprintf("msg_fake_%d", fakeIDs.Add(1))
	}
	s.mu.Unlock()
	switch {
	case resp.Status != 0:
		for name, values := range resp.Header {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		fail(w, resp.Status, resp.ErrorBody)
	case head.Stream:
		s.stream(w, r, id, head.Model, resp)
	case resp.ErrorBody != nil:
		fail(w, http.StatusInternalServerError, resp.ErrorBody)
	default:
		writeJSON(w, message(id, head.Model, resp, resp.Content))
	}
}

func (s *Server) hijackClose(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		s.t.Errorf("anthropictest: hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func fail(w http.ResponseWriter, status int, body json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusTooManyRequests {
		w.Header().Set("X-Should-Retry", "false")
	}
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
		"stop_reason": stop, "stop_details": orNull(resp.StopDetails), "stop_sequence": nil, "usage": orNull(resp.Usage), "context_management": orNull(resp.ContextManagement),
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

func (s *Server) stream(w http.ResponseWriter, r *http.Request, id, model string, resp Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	out := sse{w, flusher}
	usage := resp.Usage
	if len(resp.StartUsage) > 0 {
		usage = resp.StartUsage
	}
	start := message(id, model, resp, nil)
	delete(start, "context_management")
	start["stop_reason"] = nil
	start["usage"] = orNull(usage)
	out.send("message_start", map[string]any{"type": "message_start", "message": start})
	if resp.InterruptAfterStart {
		s.hijackClose(w)
		return
	}
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
	if resp.Stall {
		<-r.Context().Done()
		return
	}
	delta := map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp.StopReason, "stop_details": orNull(resp.StopDetails), "stop_sequence": nil},
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

func fixedJSONRequest(body []byte) error {
	var req struct {
		Model        string            `json:"model"`
		MaxTokens    int64             `json:"max_tokens"`
		System       json.RawMessage   `json:"system"`
		Messages     []json.RawMessage `json:"messages"`
		Stream       bool              `json:"stream"`
		OutputConfig struct {
			Effort string                     `json:"effort"`
			Format anthropic.JSONOutputFormat `json:"format"`
		} `json:"output_config"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return err
	}
	switch {
	case req.Model == "":
		return fmt.Errorf("missing model")
	case req.MaxTokens <= 0:
		return fmt.Errorf("max_tokens %d", req.MaxTokens)
	case len(req.Messages) != 1:
		return fmt.Errorf("%d messages, want 1", len(req.Messages))
	case req.OutputConfig.Format.Type != anthropic.FormatJSONSchema:
		return fmt.Errorf("output format type %q", req.OutputConfig.Format.Type)
	}
	wire, err := anthropic.JSONOutputSchema(req.OutputConfig.Format.Schema)
	if err != nil {
		return err
	}
	if !bytes.Equal(wire, req.OutputConfig.Format.Schema) {
		return fmt.Errorf("schema is not in wire form")
	}
	return nil
}
