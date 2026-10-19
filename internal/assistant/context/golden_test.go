package context_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const slice = "anthropic"

// limitOf is the thread context limit each golden scenario ran under.
var limitOf = map[string]int64{"context_limit_1m_clear_edits": 1_000_000, "project_thread": 200_000}

type goldenUsage struct {
	ClearEdits    int64  `json:"clear_edits"`
	ClearedTokens int64  `json:"cleared_tokens"`
	Compaction    int64  `json:"compaction"`
	Effort        string `json:"effort"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	Kind          string `json:"kind"`
}

func TestGoldenRequestConfigAndClears(t *testing.T) {
	for _, entry := range golden.LoadManifest(t, slice).Scenarios {
		t.Run(entry.Name, func(t *testing.T) {
			s := golden.LoadScenario(t, slice, entry.Name)
			limit, ok := limitOf[entry.Name]
			if !ok {
				limit = threads.DefaultContextLimit
			}
			want := actx.ContextManagement(actx.LimitsFor(limit))
			for i, ex := range s.Exchanges {
				body := bodyOf(t, ex)
				if body.ContextManagement == nil {
					if entry.Name != "client_compaction" || i != 1 || ex.Request.Headers["anthropic-beta"] != "" {
						t.Errorf("exchange %d has no context_management", i)
					}
					continue
				}
				golden.EqualJSON(t, body.ContextManagement, want)
				if got := strings.Join(actx.Betas(), ","); ex.Request.Headers["anthropic-beta"] != got {
					t.Errorf("exchange %d: anthropic-beta %q, Betas %q", i, ex.Request.Headers["anthropic-beta"], got)
				}
			}
			if len(s.Usage) != len(s.Exchanges) {
				return
			}
			for i, ex := range s.Exchanges {
				var msg anthropic.Message
				if err := json.Unmarshal(ex.Response, &msg); err != nil {
					t.Fatal(err)
				}
				var u goldenUsage
				if err := json.Unmarshal(s.Usage[i], &u); err != nil {
					t.Fatal(err)
				}
				if n, cleared := actx.ClearsOf(&msg); n != u.ClearEdits || cleared != u.ClearedTokens {
					t.Errorf("exchange %d: ClearsOf = %d, %d; usage row %d, %d", i, n, cleared, u.ClearEdits, u.ClearedTokens)
				}
			}
		})
	}
}

func TestContextManagementMatchesPythonBytes(t *testing.T) {
	got := string(actx.ContextManagement(actx.LimitsFor(90_000)))
	instructions := py.JSONDumps(actx.CompactionInstructions, compact)
	want := `{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":16875},"keep":{"type":"tool_uses","value":3},` +
		`"clear_at_least":{"type":"input_tokens","value":4500},"clear_tool_inputs":false,"exclude_tools":["recall"]},` +
		`{"type":"compact_20260112","trigger":{"type":"input_tokens","value":78750},"pause_after_compaction":true,"instructions":` + instructions + `}]}`
	if got != want {
		t.Errorf("ContextManagement =\n%s\nwant\n%s", got, want)
	}
	for _, needle := range []string{"result_id", "draft"} {
		if !strings.Contains(actx.CompactionInstructions, needle) {
			t.Errorf("compaction instructions lack %q", needle)
		}
	}
}

// TestServerCompactionGolden replays server_compaction: the request after the compaction pause is the
// compaction block, the manifest with the operator's stamped message, then the last complete unit without
// thinking.
func TestServerCompactionGolden(t *testing.T) {
	s := golden.LoadScenario(t, slice, "server_compaction")
	w := newWorld(t)
	w.startRun()
	first := goldenMessages(t, s.Exchanges[0])
	operator, stampBlock := first[0].Content[0], withoutCacheControl(t, first[0].Content[1])
	w.user(operator)
	w.assistant(asJournaled(t, responseOf(t, s.Exchanges[0]).Content)...)
	second := goldenMessages(t, s.Exchanges[1])
	w.answer("toolu_01", "profile_get", contentOf(t, dict(t, withoutCacheControl(t, second[2].Content[0]))))
	sent := stamp(t, w.active(), stampBlock)
	golden.EqualJSON(t, plain(t, second), plain(t, sent))

	compaction := responseOf(t, s.Exchanges[1])
	if compaction.StopReason != anthropic.StopCompaction {
		t.Fatalf("golden stop %q", compaction.StopReason)
	}
	w.assistant(compaction.Content...)
	if _, err := actx.ServerCompacted(t.Context(), w.d.Write, w.thread, w.run, compaction.Content, sent, w.tick()); err != nil {
		t.Fatal(err)
	}
	after := w.active()
	golden.EqualJSON(t, plain(t, goldenMessages(t, s.Exchanges[2])), plain(t, after))
	if p := w.projection(); p.Kind != actx.KindServer || p.BaseSeq != 5 {
		t.Errorf("projection %+v", p)
	}
	if got := thinkingBlocks(t, after); len(got) != 0 {
		t.Errorf("thinking survived compaction: %s", got)
	}
	if got := len(w.messages()); got != 4 {
		t.Errorf("journal has %d messages, want 4 (never rewritten)", got)
	}
}

// TestClientCompactionGolden replays client_compaction: the tool-free, thinking-free summary request is the
// golden body byte for byte as JSON, it is metered as a compaction, and the next request is the summary,
// the manifest and the operator's stamped message.
func TestClientCompactionGolden(t *testing.T) {
	s := golden.LoadScenario(t, slice, "client_compaction")
	w := newWorld(t)
	w.startRun()
	first := goldenMessages(t, s.Exchanges[0])
	operator, stampBlock := first[0].Content[0], withoutCacheControl(t, first[0].Content[1])
	w.user(operator)
	w.assistant(asJournaled(t, responseOf(t, s.Exchanges[0]).Content)...)
	flat := textOf(t, goldenMessages(t, s.Exchanges[1])[2].Content[0])
	rest, ok := strings.CutPrefix(flat, "[tool_result] ")
	if !ok {
		t.Fatalf("golden summary request: %q", flat)
	}
	result := dict(t, json.RawMessage(rest))
	useID, _ := result.Get("tool_use_id")
	content, _ := result.Get("content")
	w.answer(useID.(string), "profile_get", content.(string))
	active := stamp(t, w.active(), stampBlock)

	srv := anthropictest.New(t)
	reply, err := anthropictest.FromGolden(s.Exchanges[1].Response)
	if err != nil {
		t.Fatal(err)
	}
	srv.Push(reply)
	hard := actx.LimitsFor(threads.DefaultContextLimit).Hard
	if _, err := actx.ClientCompact(t.Context(), w.d.Write, srv.Client(), model, active, w.thread, w.run, threads.Chat, hard, w.tick()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	want := s.Exchanges[1].Request
	if got := reqs[0]; got.Method != want.Method || got.Path != want.Path || got.Query != want.Query {
		t.Errorf("request %s %s?%s, golden %s %s?%s", got.Method, got.Path, got.Query, want.Method, want.Path, want.Query)
	}
	for _, h := range []string{"anthropic-beta", "anthropic-version", "content-type"} {
		if g := strings.Join(reqs[0].Header.Values(h), ","); g != want.Headers[h] {
			t.Errorf("header %s = %q, golden %q", h, g, want.Headers[h])
		}
	}
	golden.EqualJSON(t, want.Body, reqs[0].Body)

	var row goldenUsage
	if err := w.d.Read.QueryRowContext(t.Context(),
		"SELECT clear_edits, cleared_tokens, compaction, effort, input_tokens, output_tokens, kind FROM usage WHERE run_id = ?", w.run).
		Scan(&row.ClearEdits, &row.ClearedTokens, &row.Compaction, &row.Effort, &row.InputTokens, &row.OutputTokens, &row.Kind); err != nil {
		t.Fatal(err)
	}
	var wantRow goldenUsage
	if err := json.Unmarshal(s.Usage[1], &wantRow); err != nil {
		t.Fatal(err)
	}
	if row != wantRow {
		t.Errorf("usage row %+v, golden %+v", row, wantRow)
	}
	golden.EqualJSON(t, plain(t, goldenMessages(t, s.Exchanges[2])), plain(t, w.active()))
	if p := w.projection(); p.Kind != actx.KindClient || p.BaseSeq != 4 {
		t.Errorf("projection %+v", p)
	}
}
