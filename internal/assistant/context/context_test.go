package context_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var medium = strings.Repeat("m", 1500)

func TestThresholdsFollowTheThreadContextLimit(t *testing.T) {
	if got := actx.LimitsFor(200_000); got != (actx.Limits{Hard: 200_000, Preflight: 190_000, Compact: 175_000, Clear: 37_500, ClearAtLeast: 10_000}) {
		t.Errorf("LimitsFor(200K) = %+v", got)
	}
	if got := actx.LimitsFor(1_000_000); got.Hard != 1_000_000 || got.Clear != 187_500 || got.ClearAtLeast != 50_000 || got.Compact != 875_000 {
		t.Errorf("LimitsFor(1M) = %+v", got)
	}
	if got := actx.LimitsFor(5_000_000).Hard; got != actx.ModelContext {
		t.Errorf("LimitsFor(5M).Hard = %d", got)
	}
	if got := actx.LimitsFor(threads.DefaultContextLimit); got.Clear != 16_875 || got.ClearAtLeast != 4_500 || got.Compact != 78_750 {
		t.Errorf("LimitsFor(90K) = %+v", got)
	}
	if got := actx.Betas(); !reflect.DeepEqual(got, []string{"context-management-2025-06-27", "compact-2026-01-12"}) {
		t.Errorf("Betas = %v", got)
	}
}

func TestEstimatesCountCodePointsOfPythonJSON(t *testing.T) {
	if got := actx.EstimateTokens("ééééé"); got != 2 {
		t.Errorf("EstimateTokens = %d", got)
	}
	if got := actx.EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d", got)
	}
	n, err := actx.EstimateContent([]json.RawMessage{json.RawMessage(`{"type":"text","text":"é"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if want := actx.EstimateTokens(`[{"type": "text", "text": "é"}]`); n != want {
		t.Errorf("EstimateContent = %d, want %d", n, want)
	}
	if got := actx.Serialize(py.Dict{{Key: "a", Value: []any{int64(1), "é"}}}); got != `{"a": [1, "é"]}` {
		t.Errorf("Serialize = %s", got)
	}
}

func TestUsageLedgerRecordsServerClears(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	var msg anthropic.Message
	reply := anthropictest.Reply(anthropic.StopEndTurn, text("hi")).WithContextManagement(anthropictest.Cleared(30_000, 4))
	if err := json.Unmarshal(reply.ContextManagement, &msg.ContextManagement); err != nil {
		t.Fatal(err)
	}
	edits, cleared := actx.ClearsOf(&msg)
	entry := usage.Entry{TS: w.tick(), ThreadID: w.thread, RunID: w.run, Kind: threads.Chat, Model: model, Effort: threads.Medium, ClearEdits: edits, ClearedTokens: cleared}
	if _, err := usage.Record(t.Context(), w.d.Write, entry, msg.Usage); err != nil {
		t.Fatal(err)
	}
	var row [3]int64
	if err := w.d.Read.QueryRow("SELECT clear_edits, cleared_tokens, compaction FROM usage WHERE run_id = ?", w.run).Scan(&row[0], &row[1], &row[2]); err != nil {
		t.Fatal(err)
	}
	if row != [3]int64{1, 30_000, 0} {
		t.Errorf("usage row %v", row)
	}
	if n, c := actx.ClearsOf(&anthropic.Message{}); n != 0 || c != 0 {
		t.Errorf("ClearsOf(no edits) = %d, %d", n, c)
	}
}

func TestHistoryTokensSumsTheTopLevelUsage(t *testing.T) {
	u := anthropic.Usage{InputTokens: 2_000, CacheCreationInputTokens: 300, CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: 100, Ephemeral1hInputTokens: 200},
		CacheReadInputTokens: 10_000, OutputTokens: 50, Iterations: []anthropic.Iteration{{Type: "compaction", InputTokens: 300_000}}}
	if got := actx.HistoryTokens(u); got != 2_000+100+200+10_000+50 {
		t.Errorf("HistoryTokens = %d", got)
	}
}

func TestIngestStubsLargeResults(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	ctx := t.Context()
	call := w.call("tu1", "medium")
	body, id, err := actx.Ingest(ctx, w.d.Write, w.run, call, "probe", py.Dict{{Key: "echo", Value: int64(1)}}, w.tick())
	if err != nil || id != nil || body != `{"echo": 1}` {
		t.Errorf("small Ingest = %q, %v, %v", body, id, err)
	}
	big := strings.Repeat("é", 8001)
	body, id, err = actx.Ingest(ctx, w.d.Write, w.run, call, "medium", big, w.tick())
	if err != nil || id == nil {
		t.Fatalf("Ingest = %v, %v", id, err)
	}
	want := `{"result_id": 1, "tool": "medium", "tokens": 2001, "preview": "` + strings.Repeat("é", 600) + `", "note": "` + actx.StubNote + `"}`
	if body != want {
		t.Errorf("stub = %s", body)
	}
	var tokens, size int64
	var sum, stored string
	if err := w.d.Read.QueryRow("SELECT tokens, bytes, sha256, body FROM tool_results WHERE result_id = ?", *id).Scan(&tokens, &size, &sum, &stored); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(big))
	if tokens != 2001 || size != int64(len(big)) || sum != hex.EncodeToString(digest[:]) || stored != big {
		t.Errorf("tool_results row %d %d %s", tokens, size, sum)
	}
}

// TestServerCompactionMidToolLoopKeepsOnlyCompleteUnits: sent at compaction is [hello, tu1, tr1, tu2, tr2];
// the last three would start at tr1, whose tool_use is gone, so the tail begins at the tu2 unit instead.
func TestServerCompactionMidToolLoopKeepsOnlyCompleteUnits(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.user(text("hello"))
	w.assistant(thinking("t1", "s1"), toolUse("tu1", "probe", map[string]any{"x": 1}))
	w.answer("tu1", "probe", `{"echo": 1}`)
	w.assistant(thinking("t2", "s2"), toolUse("tu2", "probe", map[string]any{"x": 2}))
	w.answer("tu2", "probe", `{"echo": 2}`)
	sent := w.active()
	validRequest(t, sent, true, true)
	block := anthropictest.Compaction("Goal: answer hello. Seen: probe x=1 and x=2.", "sig")
	w.assistant(block)
	if _, err := actx.ServerCompacted(t.Context(), w.d.Write, w.thread, w.run, []json.RawMessage{block}, sent, w.tick()); err != nil {
		t.Fatal(err)
	}
	after := w.active()
	validRequest(t, after, true, true)
	if len(after) != 4 || string(plain(t, after[:1])) != string(plain(t, []actx.Message{{Role: journal.Assistant, Content: []json.RawMessage{block}}})) {
		t.Fatalf("after compaction %s", plain(t, after))
	}
	if len(after[1].Content) != 1 || !strings.HasPrefix(textOf(t, after[1].Content[0]), "Context manifest") {
		t.Errorf("manifest message %s", plain(t, after[1:2]))
	}
	golden.EqualJSON(t, plain(t, []actx.Message{{Role: journal.Assistant, Content: []json.RawMessage{toolUse("tu2", "probe", map[string]any{"x": 2})}}}), plain(t, after[2:3]))
	if got := resultOf(t, after[3:], "tu2"); got == nil || len(after[3].Content) != 1 {
		t.Errorf("tail result %s", plain(t, after[3:]))
	}
	if strings.Contains(string(plain(t, after)), "tu1") || len(thinkingBlocks(t, after)) != 0 {
		t.Errorf("tu1 or thinking survived: %s", plain(t, after))
	}
	w.assistant(thinking("t3", "s3"), toolUse("tu3", "probe", map[string]any{"x": 3}))
	w.answer("tu3", "probe", `{"echo": 3}`)
	next := w.active()
	validRequest(t, next, true, true)
	golden.EqualJSON(t, plain(t, after), plain(t, next[:4]))
	if got := resultOf(t, next[5:], "tu3"); got == nil {
		t.Error("tu3 result missing")
	}
	w.assistant(text("done"))
	w.startRun()
	w.user(text("next"))
	later := w.active()
	validRequest(t, later, true, true)
	golden.EqualJSON(t, plain(t, after), plain(t, later[:4]))
}

func TestPreservedTailNeverStartsWithALoneToolResult(t *testing.T) {
	msg := func(role string, blocks ...json.RawMessage) actx.Message {
		return actx.Message{Role: role, Content: blocks}
	}
	hello := msg(journal.User, text("hello"))
	a1 := msg(journal.Assistant, thinking("t", "s"), toolUse("tu1", "probe", nil))
	r1 := msg(journal.User, toolResult("tu1", "1"))
	a2 := msg(journal.Assistant, thinking("t", "s"), toolUse("tu2", "probe", nil))
	r2 := msg(journal.User, toolResult("tu2", "2"))
	bare1 := msg(journal.Assistant, toolUse("tu1", "probe", nil))
	bare2 := msg(journal.Assistant, toolUse("tu2", "probe", nil))
	for _, c := range []struct{ sent, want []actx.Message }{
		{[]actx.Message{hello, a1, r1}, []actx.Message{hello, bare1, r1}},
		{[]actx.Message{hello, a1, r1, a2, r2}, []actx.Message{bare2, r2}},
		{[]actx.Message{a1, r1, hello}, []actx.Message{bare1, r1, hello}},
		{[]actx.Message{r1, hello}, []actx.Message{hello}},
		{[]actx.Message{a1, r1, a2, r2, hello}, []actx.Message{bare2, r2, hello}},
	} {
		got := actx.Preserved(c.sent)
		if string(plain(t, got)) != string(plain(t, c.want)) {
			t.Errorf("Preserved(%s) = %s, want %s", plain(t, c.sent), plain(t, got), plain(t, c.want))
		}
	}
}

func TestClientCompactionMidToolLoopSendsAValidToolFreeRequest(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.user(text("hello there"))
	w.assistant(thinking("t1", "s1"), toolUse("tu1", "probe", map[string]any{"x": 1}))
	w.answer("tu1", "probe", `{"echo": 1}`)
	w.assistant(thinking("t2", "s2"), toolUse("tu2", "medium", nil))
	w.answer("tu2", "medium", medium)
	srv := anthropictest.New(t, anthropictest.Reply(anthropic.StopEndTurn, text("SUMMARY: goal hello; probe echoed 1; medium read.")))
	before := w.active()
	if _, err := actx.ClientCompact(t.Context(), w.d.Write, srv.Client(), model, before, w.thread, w.run, threads.Chat, actx.LimitsFor(threads.DefaultContextLimit).Hard, w.tick()); err != nil {
		t.Fatal(err)
	}
	var params anthropic.Params
	if err := json.Unmarshal(srv.Requests()[0].Body, &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Tools) != 0 || params.Thinking != nil || params.OutputConfig.Effort != "low" || params.MaxTokens != actx.SummaryMaxTokens || string(params.System) != `"`+actx.SummarySystem+`"` {
		t.Errorf("summary request %s", srv.Requests()[0].Body)
	}
	flat := make([]actx.Message, len(params.Messages))
	for i, raw := range params.Messages {
		var m goldenMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		flat[i] = actx.Message{Role: m.Role, Content: m.Content}
	}
	validRequest(t, flat, false, false)
	if roles := rolesOf(flat); !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "user"}) {
		t.Errorf("roles %v", roles)
	}
	for _, m := range flat {
		for _, b := range m.Content {
			if kindOf(t, b) != actx.Text {
				t.Errorf("non-text block %s", b)
			}
		}
	}
	if s := textOf(t, flat[1].Content[0]); !strings.HasPrefix(s, "[tool_use]") || !strings.Contains(s, `"tu1"`) {
		t.Errorf("flattened tool_use %q", s)
	}
	if s := textOf(t, flat[2].Content[0]); !strings.HasPrefix(s, "[tool_result]") {
		t.Errorf("flattened tool_result %q", s)
	}
	if s := textOf(t, flat[4].Content[0]); !strings.Contains(s, medium) {
		t.Error("medium result truncated although the request fits")
	}
	if last := flat[4].Content; !strings.HasSuffix(textOf(t, last[len(last)-1]), "Write the summary now.") {
		t.Error("summary ask missing")
	}
	resumed := w.active()
	if len(resumed) != 1 || len(resumed[0].Content) != 3 {
		t.Fatalf("resumed %s", plain(t, resumed))
	}
	if !strings.Contains(textOf(t, resumed[0].Content[0]), "SUMMARY: goal hello") || textOf(t, resumed[0].Content[2]) != "hello there" {
		t.Errorf("resumed %s", plain(t, resumed))
	}
	if p := w.projection(); p.Kind != actx.KindClient {
		t.Errorf("projection %+v", p)
	}
	var compactions int
	if err := w.d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE run_id = ? AND compaction = 1 AND effort = 'low'", w.run).Scan(&compactions); err != nil || compactions != 1 {
		t.Errorf("compaction usage rows %d, %v", compactions, err)
	}
	w.assistant(thinking("t3", "s3"), toolUse("tu3", "probe", map[string]any{"x": 3}))
	w.answer("tu3", "probe", `{"echo": 3}`)
	validRequest(t, w.active(), true, true)
	w.assistant(text("done"))
	w.startRun()
	w.user(text("next"))
	validRequest(t, w.active(), true, true)
}

func TestClientCompactionFailsOnAnEmptySummary(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.user(text("hello"))
	srv := anthropictest.New(t, anthropictest.Reply(anthropic.StopEndTurn, text("  \n")))
	_, err := actx.ClientCompact(t.Context(), w.d.Write, srv.Client(), model, w.active(), w.thread, w.run, threads.Chat, 90_000, w.tick())
	var ce actx.ContextError
	if !errors.As(err, &ce) || ce != "compaction summary was empty" {
		t.Errorf("err = %v", err)
	}
	var rows int
	if err := w.d.Read.QueryRow("SELECT COUNT(*) FROM usage WHERE compaction = 1").Scan(&rows); err != nil || rows != 1 {
		t.Errorf("the failed summary must still be metered: %d, %v", rows, err)
	}
	if p := w.projection(); p.ID != 0 {
		t.Errorf("projection written: %+v", p)
	}
}

func TestSummaryRequestAfterAServerCompactionStartsWithAUserMessage(t *testing.T) {
	seq := int64(9)
	run := int64(1)
	active := []actx.Message{
		{Role: journal.Assistant, Content: []json.RawMessage{anthropictest.Compaction("earlier summary", "sig")}},
		{Role: journal.User, Content: []json.RawMessage{text("manifest"), text("go on")}},
		{Role: journal.Assistant, Content: []json.RawMessage{thinking("t", "s"), toolUse("tu9", "probe", nil)}, Seq: &seq, RunID: &run},
		{Role: journal.User, Content: []json.RawMessage{toolResult("tu9", strings.Repeat("x", 2000))}, Seq: &seq, RunID: &run},
	}
	out, err := actx.Shrink(active, 10)
	if err != nil {
		t.Fatal(err)
	}
	if roles := rolesOf(out); !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "user"}) {
		t.Fatalf("roles %v", roles)
	}
	golden.EqualJSON(t, plain(t, []actx.Message{{Role: journal.Assistant, Content: []json.RawMessage{text("[earlier context summary] earlier summary")}}}), plain(t, out[1:2]))
	all := string(plain(t, out))
	if !strings.Contains(textOf(t, out[4].Content[0]), "[truncated for compaction]") || strings.Contains(all, "thinking") {
		t.Errorf("shrunk %s", all)
	}
	want := `[tool_result] {"tool_use_id": "tu9", "content": "` + strings.Repeat("x", 600) + ` [truncated for compaction]"}`
	if got := textOf(t, out[4].Content[0]); got != want {
		t.Errorf("truncated result %q", got)
	}
}

func rolesOf(messages []actx.Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Role
	}
	return out
}

func statusOf(t *testing.T, w *world) py.Dict {
	t.Helper()
	s, err := actx.Status(t.Context(), w.d.Read, w.thread)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, d py.Dict, key string) any {
	t.Helper()
	v, ok := d.Get(key)
	if !ok {
		t.Fatalf("%s missing from %s", key, py.JSONDumps(d))
	}
	return v
}

func TestDropIsStagedThenCommittedAtTheNextTurnAndStaysRecallable(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	w.startRun()
	w.user(text("first"))
	w.assistant(thinking("a", "sa"), toolUse("tu1", "medium", nil))
	w.answer("tu1", "medium", medium)
	w.assistant(thinking("b", "sb"), toolUse("tu2", "context_status", nil))
	dashboard := statusOf(t, w)
	var item py.Dict
	for _, v := range get(t, dashboard, "items").([]any) {
		if d := v.(py.Dict); get(t, d, "id") == "tu1" {
			item = d
		}
	}
	want := py.Dict{{Key: "id", Value: "tu1"}, {Key: "seq", Value: int64(3)}, {Key: "tool", Value: "medium"}, {Key: "tokens", Value: int64(375)},
		{Key: "state", Value: "active"}, {Key: "result_id", Value: nil}}
	if py.JSONDumps(item) != py.JSONDumps(want) {
		t.Errorf("item %s", py.JSONDumps(item))
	}
	if get(t, dashboard, "approx_tokens").(int64) <= 375 || get(t, dashboard, "turn_start_seq") != int64(1) {
		t.Errorf("dashboard %s", py.JSONDumps(dashboard))
	}
	w.answer("tu2", "context_status", py.JSONDumps(dashboard, compact))
	w.assistant(toolUse("tu3", "context_drop", map[string]any{"ids": []string{"tu1"}}))
	staged, err := actx.Drop(ctx, w.d.Write, w.thread, w.run, []any{"tu1"}, w.tick())
	if err != nil {
		t.Fatal(err)
	}
	if savings := get(t, staged, "staged_savings").(int64); py.JSONDumps(get(t, staged, "staged_drop")) != `["tu1"]` || savings <= 0 || savings >= actx.CommitSavings {
		t.Errorf("Drop = %s", py.JSONDumps(staged))
	}
	if got := contentOf(t, resultOf(t, w.active(), "tu1")); got != medium {
		t.Error("a staged drop changed the context before commit")
	}
	if r, _ := w.ledger().Get("tu1"); r.State != actx.StagedDrop {
		t.Errorf("ledger %+v", r)
	}
	w.answer("tu3", "context_drop", py.JSONDumps(staged))
	w.assistant(text("noted"))

	w.startRun()
	w.user(text("second"))
	c := w.commit(true)
	if c == nil || !reflect.DeepEqual(c.Applied, []string{"tu1"}) {
		t.Fatalf("Commit = %+v", c)
	}
	first := w.active()
	if got := contentOf(t, resultOf(t, first, "tu1")); got != `{"result_id": 1, "dropped": true}` {
		t.Errorf("dropped result %s", got)
	}
	if string(first[1].Content[0]) != string(thinking("a", "sa")) {
		t.Errorf("thinking before the edit was stripped: %s", first[1].Content[0])
	}
	for _, b := range thinkingBlocks(t, first) {
		if string(b) == string(thinking("b", "sb")) {
			t.Error("thinking after the edit survived")
		}
	}
	if r, _ := w.ledger().Get("tu1"); r.State != actx.Dropped || r.ResultID == nil || *r.ResultID != 1 {
		t.Errorf("ledger %+v", r)
	}
	var body string
	if err := w.d.Read.QueryRow("SELECT body FROM tool_results WHERE result_id = 1").Scan(&body); err != nil || body != medium {
		t.Errorf("stored body %d chars, %v", len(body), err)
	}
	if got := contentOf(t, dict(t, w.messages()[2].Content[0])); got != medium {
		t.Error("the journal was rewritten")
	}
	if p := w.projection(); p.Kind != actx.KindCommit || p.StripFromSeq != 3 || p.StripBeforeSeq != 9 {
		t.Errorf("projection %+v", p)
	}
	if w.commit(true) != nil {
		t.Error("a second commit applied again")
	}
}

func TestCommitMidRunOnlyWithoutCurrentThinking(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	w.startRun()
	w.user(text("first"))
	w.assistant(toolUse("m1", "medium", nil))
	w.answer("m1", "medium", medium)
	w.assistant(text("got it"))

	w.startRun()
	w.user(text("second"))
	w.assistant(thinking("t", "s"), toolUse("d1", "context_drop", nil))
	if _, err := actx.Drop(ctx, w.d.Write, w.thread, w.run, []any{"m1"}, w.tick()); err != nil {
		t.Fatal(err)
	}
	w.answer("d1", "context_drop", "{}")
	if w.commit(false) != nil {
		t.Error("committed mid-run while the tool loop carries thinking")
	}
	if got := contentOf(t, resultOf(t, w.active(), "m1")); got != medium {
		t.Error("m1 dropped too early")
	}
	w.assistant(text("later"))
	if r, _ := w.ledger().Get("m1"); r.State != actx.StagedDrop {
		t.Errorf("ledger %+v", r)
	}

	w.startRun()
	w.user(text("third"))
	if w.commit(true) == nil {
		t.Fatal("the turn boundary did not commit")
	}
	if got := contentOf(t, resultOf(t, w.active(), "m1")); !strings.Contains(got, `"dropped": true`) {
		t.Errorf("m1 = %s", got)
	}
	w.assistant(text("now"))

	w.startRun()
	w.user(text("fourth"))
	w.assistant(toolUse("m2", "medium", nil))
	w.answer("m2", "medium", medium)
	w.assistant(text("x"))
	w.startRun()
	w.user(text("fifth"))
	w.assistant(toolUse("d2", "context_drop", nil))
	if _, err := actx.Drop(ctx, w.d.Write, w.thread, w.run, []any{"m2"}, w.tick()); err != nil {
		t.Fatal(err)
	}
	w.answer("d2", "context_drop", "{}")
	if w.commit(false) == nil {
		t.Fatal("no commit mid-run without thinking")
	}
	if got := contentOf(t, resultOf(t, w.active(), "m2")); !strings.Contains(got, `"dropped": true`) {
		t.Errorf("m2 = %s", got)
	}
}

func contextError(t *testing.T, err error, needle string) {
	t.Helper()
	var ce actx.ContextError
	if !errors.As(err, &ce) || !strings.Contains(string(ce), needle) {
		t.Errorf("err = %v, want a ContextError with %q", err, needle)
	}
}

func TestPinBlocksDropAndFoldReplacesCompletedTurns(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	w.startRun()
	w.user(text("one"))
	w.assistant(toolUse("p1", "probe", map[string]any{"x": 1}))
	w.answer("p1", "probe", `{"echo": 1}`)
	w.assistant(text("answer one"))

	w.startRun()
	w.user(text("two"))
	w.assistant(toolUse("pin", "context_pin", nil))
	pinned, err := actx.Pin(ctx, w.d.Write, w.thread, w.run, []any{"p1"}, w.tick())
	if err != nil || py.JSONDumps(pinned) != `{"pinned": ["p1"]}` {
		t.Fatalf("Pin = %v, %v", pinned, err)
	}
	w.answer("pin", "context_pin", py.JSONDumps(pinned))
	w.assistant(toolUse("drop", "context_drop", nil))
	_, err = actx.Drop(ctx, w.d.Write, w.thread, w.run, []any{"p1"}, w.tick())
	contextError(t, err, "pinned")
	w.answer("drop", "context_drop", err.Error())
	w.assistant(toolUse("bad", "context_fold", nil))
	_, err = actx.Fold(ctx, w.d.Write, w.thread, w.run, int64(2), int64(3), "x", nil, w.tick())
	contextError(t, err, "operator message")
	w.answer("bad", "context_fold", err.Error())
	w.assistant(toolUse("fold", "context_fold", nil))
	_, err = actx.Fold(ctx, w.d.Write, w.thread, w.run, int64(1), int64(4), "Turn one: probe x=1 said echo 1.", []any{"mail:1", int64(7)}, w.tick())
	contextError(t, err, "pinned")
	w.answer("fold", "context_fold", err.Error())
	w.assistant(text("folded"))

	w.startRun()
	w.user(text("three"))
	w.assistant(toolUse("fold2", "context_fold", nil))
	staged, err := actx.Fold(ctx, w.d.Write, w.thread, w.run, int64(5), int64(14), "Turn two: pinned p1, folds tried.", []any{"mail:2"}, w.tick())
	if err != nil || get(t, staged, "staged_fold") != "fold:5-14" || get(t, staged, "tokens_saved").(int64) <= 0 {
		t.Fatalf("Fold = %v, %v", staged, err)
	}
	w.answer("fold2", "context_fold", py.JSONDumps(staged))
	w.assistant(text("ok"))

	w.startRun()
	w.user(text("four"))
	w.commit(true)
	sent := w.active()
	validRequest(t, sent, true, true)
	if roles := rolesOf(sent); !reflect.DeepEqual(roles, []string{"user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user"}) {
		t.Fatalf("roles %v: %s", roles, plain(t, sent))
	}
	if textOf(t, sent[0].Content[0]) != "one" || field(t, sent[1].Content[0], "id") != "p1" {
		t.Errorf("first turn %s", plain(t, sent[:2]))
	}
	foldText := textOf(t, sent[4].Content[0])
	if !strings.HasPrefix(foldText, "[context fold of seq 5-14] Turn two") || !strings.Contains(foldText, "mail:2") || textOf(t, sent[4].Content[1]) != "three" {
		t.Errorf("fold message %s", plain(t, sent[4:5]))
	}
	if textOf(t, sent[len(sent)-1].Content[0]) != "four" {
		t.Error("the operator message is not last")
	}
	manifest, err := actx.Manifest(ctx, w.d.Read, w.thread)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "Fold seq 5-14: Turn two: pinned p1, folds tried. [sources: mail:2]") || !strings.Contains(manifest, "Pinned: p1 (probe)") {
		t.Errorf("manifest %s", manifest)
	}
}

func TestFoldValidation(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	w.startRun()
	w.user(text("one"))
	w.assistant(toolUse("p1", "probe", nil))
	w.answer("p1", "probe", "1")
	w.assistant(text("answer"))
	w.startRun()
	w.user(text("two"))
	fold := func(from, to, summary any, sources []any) error {
		_, err := actx.Fold(ctx, w.d.Write, w.thread, w.run, from, to, summary, sources, w.tick())
		return err
	}
	contextError(t, fold(int64(1), int64(4), " ", nil), "summary is required")
	contextError(t, fold(int64(1), int64(4), "s", []any{true}), "source_ids must be strings or integers")
	contextError(t, fold(true, int64(4), "s", nil), "from_seq and to_seq must be integers")
	contextError(t, fold(int64(4), int64(1), "s", nil), "from_seq <= to_seq")
	contextError(t, fold(int64(1), int64(9), "s", nil), "the fold must lie within seq 0-5 of the active context")
	contextError(t, fold(int64(1), int64(5), "s", nil), "only completed turns can be folded (seq < 5)")
	contextError(t, fold(int64(1), int64(2), "s", nil), "to_seq must not end on a tool call whose result follows it")
	if err := fold(int64(1), int64(4), " s ", []any{"a", int64(2)}); err != nil {
		t.Fatal(err)
	}
	contextError(t, fold(int64(1), int64(3), "s", nil), "overlaps fold fold:1-4")
	r, _ := w.ledger().Get("fold:1-4")
	if r.Synopsis == nil || *r.Synopsis != "s" || r.SourceIDsJSON == nil || *r.SourceIDsJSON != `["a",2]` {
		t.Errorf("ledger %+v", r)
	}
	items, err := actx.Items(ctx, w.d.Read, w.thread)
	if err != nil || len(items) != 0 {
		t.Errorf("items inside a staged fold = %v, %v", items, err)
	}
	_, err = actx.Pin(ctx, w.d.Write, w.thread, w.run, []any{"p1"}, w.tick())
	contextError(t, err, "unknown context item(s): p1")
	for _, ids := range []any{nil, []any{}, []any{""}, []any{int64(1)}, "p1"} {
		_, err := actx.Drop(ctx, w.d.Write, w.thread, w.run, ids, w.tick())
		contextError(t, err, "ids must be a non-empty list of context item ids (see context_status)")
	}
	if got, err := actx.CleanIDs([]any{"b", "a", "b"}); err != nil || !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("CleanIDs = %v, %v", got, err)
	}
}

func TestSystemSignatureChangeStripsThinking(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.user(text("hi"))
	other := "sig-old"
	w.appendMessage(journal.Assistant, &other, thinking("old", "so"), text("a"))
	w.user(text("again"))
	w.assistant(thinking("new", "sn"), text("b"))
	got := w.active()
	if blocks := thinkingBlocks(t, got); len(blocks) != 1 || string(blocks[0]) != string(thinking("new", "sn")) {
		t.Errorf("thinking %s", blocks)
	}
	all, _, err := actx.ActiveMessages(t.Context(), w.d.Read, w.thread, "")
	if err != nil || len(thinkingBlocks(t, all)) != 2 {
		t.Errorf("without a signature every thinking block stays: %v", err)
	}
}
