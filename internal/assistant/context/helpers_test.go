package context_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const (
	model = "claude-haiku-5-5"
	sig   = "sig-current"
)

var compact = py.JSONOpts{Compact: true, NoEnsureASCII: true}

type world struct {
	t      *testing.T
	d      *db.DB
	thread int64
	run    int64
	now    float64
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := dbtest.Schema(t)
	id, err := threads.Create(t.Context(), d.Write, 1, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, d: d, thread: id, now: 1_780_000_000}
}

func (w *world) tick() float64 {
	w.now++
	return w.now
}

func (w *world) startRun() int64 {
	w.t.Helper()
	id, err := journal.StartRun(w.t.Context(), w.d.Write, w.thread, threads.Chat, model, threads.Medium, w.tick(), nil, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	w.run = id
	return id
}

func (w *world) appendMessage(role string, systemSig *string, blocks ...json.RawMessage) int64 {
	w.t.Helper()
	seq, err := journal.AppendMessage(w.t.Context(), w.d, w.thread, &w.run, role, blocks, w.tick(), systemSig)
	if err != nil {
		w.t.Fatal(err)
	}
	return seq
}

func (w *world) user(blocks ...json.RawMessage) int64 {
	return w.appendMessage(journal.User, nil, blocks...)
}

func (w *world) assistant(blocks ...json.RawMessage) int64 {
	s := sig
	return w.appendMessage(journal.Assistant, &s, blocks...)
}

func (w *world) call(useID, name string) int64 {
	w.t.Helper()
	ctx := w.t.Context()
	id, err := journal.StartToolCall(ctx, w.d.Write, w.run, useID, name, json.RawMessage(`{}`), journal.Safe, w.tick())
	if err != nil {
		w.t.Fatal(err)
	}
	if err := journal.FinishToolCall(ctx, w.d.Write, id, journal.OK, w.tick(), nil, nil); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) answer(useID, name, content string) int64 {
	w.call(useID, name)
	return w.user(toolResult(useID, content))
}

func (w *world) active() []actx.Message {
	w.t.Helper()
	got, _, err := actx.ActiveMessages(w.t.Context(), w.d.Read, w.thread, sig)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

func (w *world) projection() actx.Projection {
	w.t.Helper()
	p, err := actx.GetProjection(w.t.Context(), w.d.Read, w.thread)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) ledger() actx.Ledger {
	w.t.Helper()
	l, err := actx.LedgerRows(w.t.Context(), w.d.Read, w.thread)
	if err != nil {
		w.t.Fatal(err)
	}
	return l
}

func (w *world) messages() []journal.Message {
	w.t.Helper()
	all, err := journal.MessagesOf(w.t.Context(), w.d.Read, w.thread, false)
	if err != nil {
		w.t.Fatal(err)
	}
	return all
}

func (w *world) commit(boundary bool) *actx.Committed {
	w.t.Helper()
	c, err := actx.Commit(w.t.Context(), w.d.Write, w.thread, w.run, w.tick(), boundary)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

func text(v string) json.RawMessage { return anthropictest.Text(v) }

func thinking(v, signature string) json.RawMessage { return anthropictest.Thinking(v, signature) }

func toolUse(id, name string, input map[string]any) json.RawMessage {
	if input == nil {
		input = map[string]any{}
	}
	return anthropictest.ToolUse(id, name, input)
}

func toolResult(id, content string) json.RawMessage {
	return json.RawMessage(py.JSONDumps(py.Dict{{Key: "type", Value: "tool_result"}, {Key: "tool_use_id", Value: id}, {Key: "content", Value: content}}, compact))
}

func dict(t *testing.T, raw json.RawMessage) py.Dict {
	t.Helper()
	v, err := py.JSONLoads(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := v.(py.Dict)
	if !ok {
		t.Fatalf("%s is not an object", raw)
	}
	return d
}

func field(t *testing.T, raw json.RawMessage, key string) any {
	t.Helper()
	v, _ := dict(t, raw).Get(key)
	return v
}

func kindOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	s, _ := field(t, raw, "type").(string)
	return s
}

func textOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	s, _ := field(t, raw, "text").(string)
	return s
}

func withoutCacheControl(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var out py.Dict
	for _, p := range dict(t, raw) {
		if p.Key != "cache_control" {
			out = append(out, p)
		}
	}
	return json.RawMessage(py.JSONDumps(out, compact))
}

func asJournaled(t *testing.T, blocks []json.RawMessage) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(blocks))
	for i, raw := range blocks {
		d := dict(t, raw)
		var ordered py.Dict
		for _, key := range []string{"type", "id", "name", "input"} {
			if v, ok := d.Get(key); ok {
				ordered = append(ordered, py.Pair{Key: key, Value: v})
			}
		}
		for _, p := range d {
			switch p.Key {
			case "type", "id", "name", "input":
			default:
				ordered = append(ordered, p)
			}
		}
		out[i] = json.RawMessage(py.JSONDumps(ordered, compact))
	}
	return out
}

func plain(t *testing.T, messages []actx.Message) []byte {
	t.Helper()
	parts := make([]string, len(messages))
	for i, m := range messages {
		content := make([]json.RawMessage, len(m.Content))
		for j, b := range m.Content {
			content[j] = withoutCacheControl(t, b)
		}
		parts[i] = string(actx.Message{Role: m.Role, Content: content}.Wire())
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

func thinkingBlocks(t *testing.T, messages []actx.Message) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, m := range messages {
		for _, b := range m.Content {
			if k := kindOf(t, b); k == actx.Thinking || k == actx.RedactedThinking {
				out = append(out, b)
			}
		}
	}
	return out
}

func resultOf(t *testing.T, messages []actx.Message, useID string) py.Dict {
	t.Helper()
	for _, m := range messages {
		for _, b := range m.Content {
			if kindOf(t, b) == actx.ToolResult && field(t, b, "tool_use_id") == useID {
				return dict(t, b)
			}
		}
	}
	t.Fatalf("no result for %s", useID)
	return nil
}

func contentOf(t *testing.T, d py.Dict) string {
	t.Helper()
	s, _ := d.Get("content")
	return actx.Serialize(s)
}

func validRequest(t *testing.T, messages []actx.Message, tools, thinks bool) {
	t.Helper()
	if len(messages) == 0 || messages[len(messages)-1].Role != journal.User {
		t.Fatal("the request must end with a user message")
	}
	if first := messages[0]; first.Role != journal.User && (len(first.Content) != 1 || kindOf(t, first.Content[0]) != actx.Compaction) {
		t.Fatalf("the request starts with %s", plain(t, messages[:1]))
	}
	pending := map[string]bool{}
	for i, m := range messages {
		if i > 0 && messages[i-1].Role == m.Role {
			t.Fatalf("message %d repeats role %s", i, m.Role)
		}
		uses := map[string]bool{}
		var results []string
		for _, b := range m.Content {
			switch k := kindOf(t, b); {
			case k == actx.ToolUse:
				uses[field(t, b, "id").(string)] = true
			case k == actx.ToolResult:
				results = append(results, field(t, b, "tool_use_id").(string))
			case !thinks && (k == actx.Thinking || k == actx.RedactedThinking):
				t.Fatalf("message %d has thinking but the request does not enable it", i)
			}
		}
		if !tools && (len(uses) > 0 || len(results) > 0) {
			t.Fatalf("message %d has tool blocks but the request defines no tools", i)
		}
		if m.Role == journal.Assistant {
			if len(results) > 0 {
				t.Fatalf("assistant message %d holds tool results", i)
			}
			pending = uses
			continue
		}
		if len(uses) > 0 || len(results) != len(pending) {
			t.Fatalf("message %d: results %v vs pending %v", i, results, pending)
		}
		for _, r := range results {
			if !pending[r] {
				t.Fatalf("message %d: result %s answers no pending use", i, r)
			}
		}
		pending = map[string]bool{}
	}
	if len(pending) > 0 {
		t.Fatalf("unanswered tool uses %v", pending)
	}
}

func stamp(t *testing.T, messages []actx.Message, block json.RawMessage) []actx.Message {
	t.Helper()
	out := make([]actx.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if m.Role == journal.User && m.RunID != nil && journal.HasText(m.Content) {
			out[i].Content = append(append([]json.RawMessage{}, m.Content...), block)
		}
	}
	return out
}

type goldenMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type goldenBody struct {
	Messages          []goldenMessage `json:"messages"`
	ContextManagement json.RawMessage `json:"context_management"`
}

type goldenResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
}

func bodyOf(t *testing.T, ex golden.Exchange) goldenBody {
	t.Helper()
	var b goldenBody
	if err := json.Unmarshal(ex.Request.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func responseOf(t *testing.T, ex golden.Exchange) goldenResponse {
	t.Helper()
	var r goldenResponse
	if err := json.Unmarshal(ex.Response, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func goldenMessages(t *testing.T, ex golden.Exchange) []actx.Message {
	t.Helper()
	b := bodyOf(t, ex)
	out := make([]actx.Message, len(b.Messages))
	for i, m := range b.Messages {
		out[i] = actx.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
