package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func chatParams() anthropic.Params {
	return anthropic.Params{
		Model:        "claude-haiku-5-5",
		MaxTokens:    16384,
		Thinking:     &anthropic.Thinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: &anthropic.OutputConfig{Effort: "medium"},
		System:       json.RawMessage(`[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}]`),
		Messages:     []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"go"}]}`)},
		Betas:        []string{anthropic.BetaContextManagement, anthropic.BetaCompaction},
	}
}

func progressOf(t *testing.T, c *anthropic.Client) (*anthropic.Message, []anthropic.Event, int) {
	t.Helper()
	var events []anthropic.Event
	calls := 0
	msg, err := c.Stream(context.Background(), chatParams(), func(e anthropic.Event) error {
		calls++
		if e.Kind != "" {
			events = append(events, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg, events, calls
}

// The events progress.Writer.observe produces for the Python SDK's stream of the same response.
func TestStreamProgressEvents(t *testing.T) {
	search := anthropictest.WebSearch("srvtoolu_01", "carnival dates 2027", "https://carnival.test/2027")
	failedSearch := json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_02","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}`)
	cite := json.RawMessage(`{"type":"web_search_result_location","url":"https://a.test","title":"A","encrypted_index":"e","cited_text":"x"}`)
	cases := []struct {
		name string
		resp anthropictest.Response
		stop string
		want []anthropic.Event
	}{
		{
			name: "text",
			resp: anthropictest.Reply(anthropic.StopEndTurn, anthropictest.Text("You are in Port of Spain.")),
			stop: anthropic.StopEndTurn,
			want: []anthropic.Event{{Kind: anthropic.EventText, Text: "You are in Port of Spain."}},
		},
		{
			name: "thinking summarized and tool_use",
			resp: anthropictest.Reply(anthropic.StopToolUse,
				anthropictest.Thinking("check the profile", "sig-1"),
				anthropictest.ToolUse("toolu_01", "profile_get", map[string]any{"key": "city"})),
			stop: anthropic.StopToolUse,
			want: []anthropic.Event{{Kind: anthropic.EventThinking, Text: "check the profile"}},
		},
		{
			name: "pause_turn with web search",
			resp: anthropictest.Reply(anthropic.StopPauseTurn, append([]json.RawMessage{anthropictest.Thinking("search first", "sig-p")}, search...)...),
			stop: anthropic.StopPauseTurn,
			want: []anthropic.Event{
				{Kind: anthropic.EventThinking, Text: "search first"},
				{Kind: anthropic.EventToolStart, ID: "srvtoolu_01", Name: "web_search", Input: json.RawMessage(`{"query":"carnival dates 2027"}`)},
				{Kind: anthropic.EventToolDone, ID: "srvtoolu_01", OK: true, Preview: "1 results"},
			},
		},
		{
			name: "pause_turn mid server tool",
			resp: anthropictest.Reply(anthropic.StopPauseTurn, search[0]),
			stop: anthropic.StopPauseTurn,
			want: []anthropic.Event{{Kind: anthropic.EventToolStart, ID: "srvtoolu_01", Name: "web_search", Input: json.RawMessage(`{"query":"carnival dates 2027"}`)}},
		},
		{
			name: "web search error result and cited text",
			resp: anthropictest.Reply(anthropic.StopEndTurn, failedSearch, anthropictest.Text("no luck", cite)),
			stop: anthropic.StopEndTurn,
			want: []anthropic.Event{
				{Kind: anthropic.EventToolDone, ID: "srvtoolu_02", OK: false, Preview: "0 results"},
				{Kind: anthropic.EventText, Text: "no luck"},
			},
		},
		{
			name: "server compaction",
			resp: anthropictest.Reply(anthropic.StopCompaction, anthropictest.Compaction("Goal: answer hello.", "sig-compaction")),
			stop: anthropic.StopCompaction,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := anthropictest.New(t, tc.resp)
			msg, events, calls := progressOf(t, srv.Client())
			if len(events) != len(tc.want) {
				t.Fatalf("events %+v, want %+v", events, tc.want)
			}
			for i := range events {
				got, want := events[i], tc.want[i]
				if want.Input != nil {
					golden.EqualJSON(t, want.Input, got.Input)
				}
				got.Input, want.Input = nil, nil
				if !reflect.DeepEqual(got, want) {
					t.Errorf("event %d = %+v, want %+v", i, got, want)
				}
			}
			if calls <= len(events) {
				t.Errorf("observe ran %d times for %d progress events; every stream event must reach it", calls, len(events))
			}
			if msg.StopReason != tc.stop {
				t.Errorf("stop_reason %q, want %q", msg.StopReason, tc.stop)
			}
			golden.EqualJSON(t, mustMarshal(t, tc.resp.Content), mustMarshal(t, msg.Content))
		})
	}
}

func TestStreamObserverErrorAborts(t *testing.T) {
	stop := errors.New("stopped by the operator")
	srv := anthropictest.New(t, anthropictest.Reply(anthropic.StopEndTurn, anthropictest.Thinking("hmm", "s"), anthropictest.Text("the answer")))
	var seen []string
	_, err := srv.Client().Stream(context.Background(), chatParams(), func(e anthropic.Event) error {
		if e.Kind != "" {
			seen = append(seen, e.Kind)
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || !reflect.DeepEqual(seen, []string{anthropic.EventThinking}) {
		t.Fatalf("err %v after %v", err, seen)
	}
}

func TestUsageIterationsAndClears(t *testing.T) {
	usage := anthropictest.Usage{
		Input: 2000, CacheRead: 10000, Output: 300, Searches: 1,
		Iterations: []anthropictest.Iteration{
			{Type: "compaction", Input: 120000, Output: 900},
			{Type: "message", Input: 2000, CacheRead: 10000, Output: 300},
		},
	}
	srv := anthropictest.New(t,
		anthropictest.Reply(anthropic.StopCompaction, anthropictest.Compaction("s", "sig")).WithUsage(usage),
		anthropictest.Reply(anthropic.StopEndTurn, anthropictest.Text("hi")).WithContextManagement(anthropictest.Cleared(30000, 4)),
	)
	c := srv.Client()
	msg, _, _ := progressOf(t, c)
	want := []anthropic.Iteration{
		{Type: "compaction", InputTokens: 120000, CacheCreation: &anthropic.CacheCreation{}, OutputTokens: 900},
		{Type: "message", InputTokens: 2000, CacheCreation: &anthropic.CacheCreation{}, CacheReadInputTokens: 10000, OutputTokens: 300},
	}
	if !reflect.DeepEqual(msg.Usage.Iterations, want) {
		t.Errorf("iterations %+v, want %+v", msg.Usage.Iterations, want)
	}
	if msg.Usage.InputTokens != 2000 || msg.Usage.ServerToolUse == nil || msg.Usage.ServerToolUse.WebSearchRequests != 1 {
		t.Errorf("usage %+v", msg.Usage)
	}
	msg, err := c.Create(context.Background(), chatParams())
	if err != nil {
		t.Fatal(err)
	}
	if msg.ContextManagement == nil || !reflect.DeepEqual(msg.ContextManagement.AppliedEdits, []anthropic.AppliedEdit{{Type: "clear_tool_uses_20250919", ClearedInputTokens: 30000, ClearedToolUses: 4}}) {
		t.Errorf("context_management %+v", msg.ContextManagement)
	}
	if msg.Usage.Iterations != nil {
		t.Errorf("iterations %+v, want none", msg.Usage.Iterations)
	}
}

func TestStreamSendsBetasAndStreamFlag(t *testing.T) {
	srv := anthropictest.New(t, anthropictest.Reply(anthropic.StopEndTurn, anthropictest.Text("ok")))
	var held []anthropictest.Request
	srv.OnRequest = func(r anthropictest.Request) { held = append(held, r) }
	progressOf(t, srv.Client())
	if len(held) != 1 || srv.Remaining() != 0 {
		t.Fatalf("OnRequest saw %d requests, %d responses left", len(held), srv.Remaining())
	}
	req := srv.Requests()[0]
	if got := req.Header.Get("anthropic-beta"); got != "context-management-2025-06-27,compact-2026-01-12" {
		t.Errorf("anthropic-beta %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != anthropictest.APIKey {
		t.Errorf("x-api-key %q", got)
	}
	if body := req.JSON(t); body["stream"] != true {
		t.Errorf("stream = %v", body["stream"])
	}
}
