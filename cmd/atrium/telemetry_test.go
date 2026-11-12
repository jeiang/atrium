package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/observability"
)

type otlpSink struct {
	mu    sync.Mutex
	spans []*tracepb.Span
	raw   [][]byte
}

func (s *otlpSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body, err = io.ReadAll(zr); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	var req coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.raw = append(s.raw, body)
	for _, resource := range req.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			s.spans = append(s.spans, scope.Spans...)
		}
	}
	s.mu.Unlock()
	out, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

func (s *otlpSink) snapshot() ([]*tracepb.Span, [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*tracepb.Span(nil), s.spans...), append([][]byte(nil), s.raw...)
}

func otlpStrings(value *commonpb.AnyValue) []string {
	switch v := value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return []string{v.StringValue}
	case *commonpb.AnyValue_ArrayValue:
		var out []string
		for _, item := range v.ArrayValue.Values {
			out = append(out, otlpStrings(item)...)
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		var out []string
		for _, item := range v.KvlistValue.Values {
			out = append(out, otlpStrings(item.Value)...)
		}
		return out
	}
	return nil
}

func attrValue(span *tracepb.Span, key string) *commonpb.AnyValue {
	for _, attr := range span.Attributes {
		if attr.Key == key {
			return attr.Value
		}
	}
	return nil
}

func TestTelemetryExportsSpansAndMetricsWithoutContent(t *testing.T) {
	const (
		prompt  = "prompt-zebracorn-4f1c"
		query   = "zebracorn-query-9a2d"
		subject = "zebracorn-subject-7b3e"
		body    = "zebracorn-body-2c8f"
		address = "sender@zebracorn.invalid"
	)
	forbidden := []string{prompt, query, subject, body, address}

	sink := &otlpSink{}
	receiver := httptest.NewServer(sink)
	t.Cleanup(receiver.Close)

	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			vector := make([]float64, 768)
			vector[0] = 1
			data[i] = map[string]any{"index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(embedder.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	api := anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("call-1", "mail_search", map[string]any{"query": query})),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)
	setEnv(t, anthropic.BaseURLEnv, api.URL)
	setEnv(t, "ATRIUM_HOST_CONFIG", embeddingHost(t, dir, embedder.URL+"/v1", "embeddinggemma", ""))
	setEnv(t, settingEnv(observability.TracesEndpointKey), receiver.URL+"/insert/opentelemetry/v1/traces")

	dbPath := filepath.Join(dir, "atrium.db")
	d := mustConnect(t, dbPath, db.Key{})
	if err := db.Migrate(context.Background(), d, dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT OR IGNORE INTO accounts (name, kind) VALUES ('icloud', 'icloud')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT OR IGNORE INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'INBOX', 'INBOX', 'inbox')"); err != nil {
		t.Fatal(err)
	}
	dbtest.AddMessage(t, d, 1, body, map[string]any{
		"subject": subject, "from_addr": address, "from_name": "Zebracorn Sender",
	})

	opsBase := opsAddr(t)
	base, _, stopServe := startServeStoppable(t, dir)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status, _, _ := fetch(t, client, "GET", base+"/chat", nil); status != http.StatusOK {
		t.Fatalf("GET /chat = %d", status)
	}
	status, _, _ := fetch(t, client, "POST", base+"/chat", url.Values{"text": {prompt}})
	if status != http.StatusSeeOther {
		t.Fatalf("POST /chat = %d", status)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var state string
		if err := d.Read.QueryRow("SELECT state FROM runs ORDER BY id DESC LIMIT 1").Scan(&state); err == nil && state != "running" && state != "interrupted" {
			if state != "done" {
				t.Fatalf("run finished in state %q", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, scrape, _ := fetch(t, http.DefaultClient, "GET", opsBase+"/metrics", nil)
	for _, name := range []string{
		"atrium_http_requests_total", "atrium_http_request_duration_seconds",
		"atrium_llm_requests_total", "atrium_llm_tokens_total", "atrium_llm_cost_usd_total",
		"atrium_runs_total", "atrium_run_steps", "atrium_run_duration_seconds",
		"atrium_tool_calls_total", "atrium_tool_duration_seconds",
		"atrium_db_transactions_total", "atrium_queue_depth",
		"atrium_integration_state", "atrium_action_links_outstanding",
		"atrium_endpoint_probe_ok", "atrium_notifications_undelivered",
	} {
		if !strings.Contains(scrape, "# TYPE "+name+" ") {
			t.Errorf("ops /metrics lacks %s", name)
		}
	}
	if !strings.Contains(scrape, `atrium_tool_calls_total{tool="mail_search",ok="true"}`) {
		t.Errorf("tool metric missing:\n%s", scrape)
	}
	if !strings.Contains(scrape, `atrium_runs_total{kind="chat",state="done"}`) {
		t.Errorf("run metric missing:\n%s", scrape)
	}
	stopServe()

	spans, raw := sink.snapshot()
	if len(spans) == 0 {
		t.Fatal("no spans reached the OTLP receiver")
	}
	names := map[string]bool{}
	for _, span := range spans {
		names[span.Name] = true
		for _, attr := range span.Attributes {
			for _, value := range otlpStrings(attr.Value) {
				for _, secret := range forbidden {
					if strings.Contains(value, secret) {
						t.Errorf("span %q attribute %q carries %q", span.Name, attr.Key, secret)
					}
				}
			}
		}
		if status := span.Status; status != nil {
			for _, secret := range forbidden {
				if strings.Contains(status.Message, secret) {
					t.Errorf("span %q status carries %q", span.Name, secret)
				}
			}
		}
		for _, secret := range forbidden {
			if strings.Contains(span.Name, secret) {
				t.Errorf("span name carries %q", secret)
			}
		}
	}
	for _, want := range []string{"agent.run", "agent.step", "gen_ai.request", "tool.call"} {
		if !names[want] {
			t.Errorf("missing span %q; got %v", want, names)
		}
	}
	httpSeen := false
	for name := range names {
		if strings.HasPrefix(name, "POST /chat") || name == "http.server" {
			httpSeen = true
		}
	}
	if !httpSeen {
		t.Errorf("missing HTTP span; got %v", names)
	}
	for _, payload := range raw {
		for _, secret := range forbidden {
			if bytes.Contains(payload, []byte(secret)) {
				t.Errorf("OTLP payload carries %q", secret)
			}
		}
	}
	var tool *tracepb.Span
	var run *tracepb.Span
	var gen *tracepb.Span
	for _, span := range spans {
		switch span.Name {
		case "tool.call":
			tool = span
		case "agent.run":
			run = span
		case "gen_ai.request":
			gen = span
		}
	}
	if tool == nil || run == nil || gen == nil {
		t.Fatal("missing tool, run or gen_ai span")
	}
	if got := attrValue(tool, "tool.name"); got == nil || got.GetStringValue() != "mail_search" {
		t.Errorf("tool.name = %v", got)
	}
	if got := attrValue(tool, "tool.effect"); got == nil || got.GetStringValue() != "read" {
		t.Errorf("tool.effect = %v", got)
	}
	if got := attrValue(tool, "tool.ok"); got == nil || !got.GetBoolValue() {
		t.Errorf("tool.ok = %v", got)
	}
	if got := attrValue(run, "run.kind"); got == nil || got.GetStringValue() != "chat" {
		t.Errorf("run.kind = %v", got)
	}
	if got := attrValue(run, "run.state"); got == nil || got.GetStringValue() != "done" {
		t.Errorf("run.state = %v", got)
	}
	if got := attrValue(gen, "gen_ai.request.model"); got == nil || got.GetStringValue() == "" {
		t.Errorf("gen_ai.request.model = %v", got)
	}

}
