package service_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/progress"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
)

func (w *world) view(thread int64) *service.ThreadView {
	w.t.Helper()
	v, err := service.View(w.t.Context(), w.d.Read, thread)
	if err != nil || v == nil {
		w.t.Fatalf("view %d: %v", thread, err)
	}
	return v
}

func kindsOf(items []*service.Item) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Kind)
	}
	return out
}

func eventKinds(w *world, run int64) []string {
	events, err := progress.Since(w.t.Context(), w.d.Read, run, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestLoopStreamsAndJournalsProgressForTheRun(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	w.push(toolTurn(anthropictest.Thinking("plan it", "s1"), use("tu1", "probe", map[string]any{"x": 1})), reply(text("All done.")))
	thread, run := w.chat("hello", threads.Chat)
	if w.run(run).State != journal.Done {
		t.Fatal("not done")
	}
	if !w.srv.Requests()[0].JSON(t)["stream"].(bool) {
		t.Error("the loop did not stream")
	}
	items := w.view(thread).Items
	if got := kindsOf(items); !slices.Equal(got, []string{"user", "activity", "assistant"}) {
		t.Fatalf("items %v", got)
	}
	steps := items[1].Steps
	if len(steps) != 2 || steps[0].Kind != "thinking" || steps[0].Text != "plan it" || steps[1].Kind != "tool" || steps[1].Name != "probe" {
		t.Fatalf("steps %+v %+v", steps[0], steps[1])
	}
	golden := `{"echo": 1}`
	if steps[1].Result == nil || *steps[1].Result != golden || steps[1].State == nil || *steps[1].State != journal.OK {
		t.Errorf("tool step %+v", steps[1])
	}
	kinds := eventKinds(w, run)
	if kinds[0] != "run_start" || kinds[len(kinds)-1] != "run_done" || !slices.Contains(kinds, "tool_start") {
		t.Errorf("events %v", kinds)
	}
	w.push(reply(text("again")))
	w.turn(thread, "more", threads.Chat)
	if got := eventKinds(w, run); len(got) != 0 {
		t.Errorf("the next run kept the old progress: %v", got)
	}
}

func TestProgressFollowsTheStream(t *testing.T) {
	w := newWorld(t)
	w.push(reply(anthropictest.Thinking("hmm", "s"), text("the answer")))
	_, run := w.chat("go", threads.Chat)
	if got := eventKinds(w, run); !slices.Equal(got, []string{"run_start", "thinking", "text", "run_done"}) {
		t.Errorf("events %v", got)
	}
}

func TestEffortAndContextLimitAreStickyPerThreadAndReachTheAPI(t *testing.T) {
	w := newWorld(t)
	thread := w.thread(threads.Chat)
	if ok, err := threads.SetOptions(t.Context(), w.d.Write, thread, threads.XHigh, int64(200_000)); !ok || err != nil {
		t.Fatal(err)
	}
	w.push(reply(text("a")), reply(text("b")))
	w.turn(thread, "one", threads.Chat)
	w.turn(thread, "two", threads.Chat)
	for i := range 2 {
		call := w.body(i)
		if path(call, "output_config", "effort") != "xhigh" || path(call, "max_tokens") != float64(loop.MaxTokensDeep) ||
			path(call, "context_management", "edits", 1, "trigger", "value") != 175_000.0 {
			t.Errorf("request %d: %v %v", i, call["output_config"], call["max_tokens"])
		}
	}
	runs, _ := journal.RunsOf(t.Context(), w.d.Read, thread)
	if len(runs) != 2 || runs[0].Effort != "xhigh" || runs[1].Effort != "xhigh" {
		t.Errorf("runs %+v", runs)
	}
	if _, err := threads.SetOptions(t.Context(), w.d.Write, thread, threads.Low, int64(1_000_000)); err != nil {
		t.Fatal(err)
	}
	w.push(reply(text("c")))
	w.turn(thread, "three", threads.Chat)
	if call := w.body(2); path(call, "max_tokens") != float64(loop.MaxTokens) || path(call, "context_management", "edits", 1, "trigger", "value") != 875_000.0 {
		t.Errorf("request 2: %v", call["max_tokens"])
	}
}

func TestCompactionThresholdFollowsTheLimit(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	thread := w.thread(threads.Chat)
	if _, err := threads.SetOptions(t.Context(), w.d.Write, thread, threads.Medium, int64(200_000)); err != nil {
		t.Fatal(err)
	}
	w.push(
		toolTurn(anthropictest.Thinking("t", "s"), use("tu1", "probe", map[string]any{"x": 1})).WithUsage(anthropictest.Usage{Input: 191_000, Output: 500}),
		reply(text("SUMMARY: goal hello.")),
		reply(text("done")),
	)
	run := w.turn(thread, "hello", threads.Chat)
	if w.run(run).State != journal.Done || len(w.srv.Requests()) != 3 {
		t.Fatalf("run %+v, %d requests", w.run(run), len(w.srv.Requests()))
	}
	if path(w.body(1), "system") != actx.SummarySystem {
		t.Error("the second request is not the summary request")
	}
	if _, err := w.d.Write.Exec("UPDATE threads SET context_limit = 1000000 WHERE id = ?", thread); err != nil {
		t.Fatal(err)
	}
	w.push(toolTurn(use("tu2", "probe", map[string]any{"x": 2})).WithUsage(anthropictest.Usage{Input: 191_000, Output: 100}), reply(text("fine")))
	run = w.turn(thread, "again", threads.Chat)
	if w.run(run).State != journal.Done || len(w.srv.Requests()) != 5 || w.body(4)["tools"] == nil {
		t.Errorf("run %+v, %d requests", w.run(run), len(w.srv.Requests()))
	}
}

func TestMeterShowsLastRequestAgainstCompactionPoint(t *testing.T) {
	w := newWorld(t)
	w.push(reply(text("a")).WithUsage(anthropictest.Usage{Input: 30_000, CacheRead: 10_000, Output: 100}))
	thread, _ := w.chat("hi", threads.Chat)
	if m := w.view(thread).Meter; m.Tokens != 40_000 || m.CompactAt != 78_750 || m.Limit != 90_000 {
		t.Errorf("meter %+v", m)
	}
}

func queueOnFirstRequest(w *world, thread int64, text string) {
	var once sync.Once
	w.srv.OnRequest = func(anthropictest.Request) {
		once.Do(func() {
			if _, queued, err := journal.QueueMessage(w.t.Context(), w.d, thread, text, now0, nil); err != nil || !queued {
				w.t.Errorf("queue: %v %v", queued, err)
			}
		})
	}
}

func TestQueuedMessageIsInjectedBeforeTheNextModelCall(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	thread, run := w.prepare("start", now0)
	queueOnFirstRequest(w, thread, "also check https://x.test/p")
	w.push(toolTurn(use("tu1", "probe", map[string]any{"x": 1})), reply(text("done")))
	w.svc.Now = clock(now0)
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{}); err != nil {
		t.Fatal(err)
	}
	if w.run(run).State != journal.Done {
		t.Fatal("not done")
	}
	last := path(w.body(1), "messages", -1)
	if path(last, "role") != "user" || path(last, "content", 0, "type") != "tool_result" {
		t.Fatalf("last message %v", last)
	}
	var texts []string
	for _, b := range path(last, "content").([]any) {
		if path(b, "type") == "text" {
			texts = append(texts, path(b, "text").(string))
		}
	}
	if len(texts) != 2 || texts[0] != "also check https://x.test/p" || !strings.HasPrefix(texts[1], "Current date") {
		t.Errorf("texts %q", texts)
	}
	if q, _ := journal.Queued(t.Context(), w.d.Read, thread); len(q) != 0 {
		t.Errorf("queue %+v", q)
	}
	if urls := w.threadRow(thread).AllowedURLs; !slices.Equal(urls, []string{"https://x.test/p"}) {
		t.Errorf("allowed %v", urls)
	}
	stored := w.messages(thread)
	if m := stored[len(stored)-2]; m.Role != journal.User || blocksOf(m.Content)[0]["text"] != "also check https://x.test/p" {
		t.Errorf("journaled %+v", m)
	}
	if _, queued, _ := journal.QueueMessage(t.Context(), w.d, thread, "late", now0, nil); queued {
		t.Error("queued without a running run")
	}
}

func TestMessageQueuedAtTheLastStepKeepsTheRunGoing(t *testing.T) {
	w := newWorld(t)
	thread, run := w.prepare("start", now0)
	queueOnFirstRequest(w, thread, "one more")
	w.push(reply(text("first answer")), reply(text("second answer")))
	w.svc.Now = clock(now0)
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{}); err != nil {
		t.Fatal(err)
	}
	if w.run(run).State != journal.Done || len(w.srv.Requests()) != 2 {
		t.Fatalf("run %+v", w.run(run))
	}
	if got := path(w.body(1), "messages", -1, "content", 0, "text"); got != "one more" {
		t.Errorf("injected %v", got)
	}
}

func TestSendQueuesWhileRunning(t *testing.T) {
	w := newWorld(t)
	release, arrived := make(chan struct{}), make(chan struct{}, 2)
	w.srv.OnRequest = func(anthropictest.Request) {
		arrived <- struct{}{}
		<-release
	}
	w.push(reply(text("ok")), reply(text("ok again")))
	thread := w.thread(threads.Chat)
	first, err := w.svc.Send(t.Context(), thread, "first", nil)
	if err != nil || first.Queued {
		t.Fatalf("first %+v %v", first, err)
	}
	<-arrived
	second, err := w.svc.Send(t.Context(), thread, "second", nil)
	if err != nil || !second.Queued {
		t.Fatalf("second %+v %v", second, err)
	}
	if v := w.view(thread); len(v.Queued) != 1 || v.Queued[0].Text != "second" || v.Active == nil {
		t.Errorf("view queued %+v active %v", v.Queued, v.Active)
	}
	close(release)
	w.svc.Wait()
	if found := w.run(first.ID); found.State != journal.Done {
		t.Fatalf("run %+v", found)
	}
	if q, _ := journal.Queued(t.Context(), w.d.Read, thread); len(q) != 0 || len(w.srv.Requests()) != 2 {
		t.Errorf("queue %+v, %d requests", q, len(w.srv.Requests()))
	}
}

func TestStopEndsTheRunAtItsNextCheck(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	thread := w.thread(threads.Chat)
	var run int64
	w.srv.OnRequest = func(anthropictest.Request) {
		if ok, err := w.svc.StopRun(t.Context(), thread, run); !ok || err != nil {
			t.Errorf("stop: %v %v", ok, err)
		}
	}
	w.push(toolTurn(use("tu1", "probe", map[string]any{"x": 1})), reply(text("never")))
	w.svc.Now = clock(now0)
	run, err := w.svc.Prepare(t.Context(), thread, "go", now0, service.Start{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Execute(t.Context(), run, journal.Limits{}); err != nil {
		t.Fatal(err)
	}
	found := w.run(run)
	if found.State != journal.Failed || !strings.Contains(deref(found.Error), "stopped by the operator") || len(w.srv.Requests()) != 1 {
		t.Errorf("run %s %q, %d requests", found.State, deref(found.Error), len(w.srv.Requests()))
	}
	w.srv.OnRequest = nil
	if ok, err := w.svc.StopRun(t.Context(), thread, run); ok || err != nil {
		t.Errorf("second stop: %v %v", ok, err)
	}
	if _, err := w.svc.StopRun(t.Context(), thread+1, run); err != service.UnknownThread(thread+1) {
		t.Errorf("stop on another thread: %v", err)
	}
}

func firstTexts(messages []journal.Message) []string {
	var out []string
	for _, m := range messages {
		s, _ := blocksOf(m.Content)[0]["text"].(string)
		out = append(out, s)
	}
	return out
}

func TestEditAndRegenerateSupersedeTheTail(t *testing.T) {
	w := newWorld(t)
	w.push(reply(text("reply-alpha")), reply(text("reply-beta")), reply(text("reply-gamma")), reply(text("reply-delta")))
	thread, _ := w.chat("hello", threads.Chat)
	w.turn(thread, "more", threads.Chat)
	w.svc.Now = nil
	if _, err := w.svc.EditLast(t.Context(), thread, "more, but different", nil); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if got := firstTexts(w.messages(thread)); !slices.Equal(got, []string{"hello", "reply-alpha", "more, but different", "reply-gamma"}) {
		t.Fatalf("live %q", got)
	}
	everything, _ := journal.MessagesOf(t.Context(), w.d.Read, thread, true)
	if everything[2].Superseded != true || everything[3].Superseded != true || firstTexts(everything[2:4])[0] != "more" {
		t.Errorf("superseded tail %+v", everything[2:4])
	}
	runs, _ := journal.RunsOf(t.Context(), w.d.Read, thread)
	var flags []bool
	for _, r := range runs {
		flags = append(flags, bool(r.Superseded))
	}
	if !slices.Equal(flags, []bool{false, true, false}) {
		t.Errorf("superseded runs %v", flags)
	}
	if _, err := w.svc.Regenerate(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if got := firstTexts(w.messages(thread)); !slices.Equal(got, []string{"hello", "reply-alpha", "more, but different", "reply-delta"}) {
		t.Fatalf("live %q", got)
	}
	if all, _ := journal.MessagesOf(t.Context(), w.d.Read, thread, true); len(all) != 7 {
		t.Errorf("%d messages in the journal", len(all))
	}
	if got := path(w.body(3), "messages", -1, "content", 0, "text"); got != "more, but different" {
		t.Errorf("regenerated answer to %v", got)
	}
	if got := kindsOf(w.view(thread).Items); !slices.Equal(got, []string{"user", "assistant", "user", "assistant"}) {
		t.Errorf("items %v", got)
	}
	empty := w.thread(threads.Chat)
	if _, err := w.svc.Regenerate(t.Context(), empty); err != service.NothingToRedo("no operator message to answer again") {
		t.Errorf("regenerate an empty thread: %v", err)
	}
	if _, err := w.svc.EditLast(t.Context(), empty, "x", nil); err != service.NothingToRedo("no operator message to edit") {
		t.Errorf("edit an empty thread: %v", err)
	}
}

func TestBranchCopiesHistoryUpToTheMessage(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	w.push(toolTurn(use("tu1", "probe", map[string]any{"x": 1})), reply(text("reply-one")), reply(text("reply-two")))
	thread, _ := w.chat("hello", threads.Chat)
	w.turn(thread, "more", threads.Chat)
	if _, err := threads.SetOptions(t.Context(), w.d.Write, thread, threads.High, int64(200_000)); err != nil {
		t.Fatal(err)
	}
	if err := threads.AddAllowedURLs(t.Context(), w.d, thread, []string{"https://a.test/"}); err != nil {
		t.Fatal(err)
	}
	messages := w.messages(thread)
	var roles []string
	for _, m := range messages {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "user", "assistant", "user", "assistant"}) {
		t.Fatalf("roles %v", roles)
	}
	child, err := w.svc.Branch(t.Context(), thread, messages[3].Seq)
	if err != nil {
		t.Fatal(err)
	}
	copied := w.messages(child)
	if len(copied) != 4 {
		t.Fatalf("%d messages copied", len(copied))
	}
	for i := range copied {
		golden := mustJSON(t, messages[i].Content)
		if string(mustJSON(t, copied[i].Content)) != string(golden) {
			t.Errorf("message %d differs", i)
		}
	}
	found := w.threadRow(child)
	if *found.BranchedFromThreadID != thread || *found.BranchedFromSeq != messages[3].Seq || found.Effort != threads.High ||
		*found.ContextLimit != 200_000 || !slices.Equal(found.AllowedURLs, []string{"https://a.test/"}) {
		t.Errorf("child %+v", found)
	}
	if v := w.view(child); v.BranchedFrom == nil || v.BranchedFrom.ID != thread {
		t.Error("view lacks the branch source")
	}
	cut, err := w.svc.Branch(t.Context(), thread, messages[1].Seq)
	if err != nil {
		t.Fatal(err)
	}
	roles = nil
	for _, m := range w.messages(cut) {
		roles = append(roles, m.Role)
	}
	if !slices.Equal(roles, []string{"user", "assistant", "user"}) {
		t.Errorf("tool turn cut %v", roles)
	}
	if _, err := w.svc.Branch(t.Context(), thread, 99); err != service.NothingToRedo("no message 99 to branch from") {
		t.Errorf("branch from a missing message: %v", err)
	}
	w.push(reply(text("branched")))
	w.turn(child, "continue", threads.Chat)
	roles = nil
	for _, m := range w.body(3)["messages"].([]any) {
		roles = append(roles, path(m, "role").(string))
	}
	if !slices.Equal(roles, []string{"user", "assistant", "user", "assistant", "user"}) {
		t.Errorf("branched request roles %v", roles)
	}
}

func TestBranchMessagesDropsAnUnansweredToolUse(t *testing.T) {
	msg := func(seq int64, role string, blocks ...json.RawMessage) journal.Message {
		return journal.Message{Seq: seq, Role: role, Content: blocks}
	}
	messages := []journal.Message{
		msg(1, journal.User, text("hi")),
		msg(2, journal.Assistant, text("looking"), use("a", "probe", nil)),
	}
	got := service.BranchMessages(messages, 2)
	if len(got) != 2 || len(got[1].Content) != 1 || string(got[1].Content[0]) != string(text("looking")) {
		t.Errorf("kept %+v", got)
	}
	if got := service.BranchMessages(messages[:1:1], 1); len(got) != 1 {
		t.Errorf("plain cut %+v", got)
	}
	only := []journal.Message{msg(1, journal.User, text("hi")), msg(2, journal.Assistant, use("a", "probe", nil))}
	if got := service.BranchMessages(only, 2); len(got) != 1 {
		t.Errorf("an assistant turn of tool_use only must go: %+v", got)
	}
}

func TestCitedTextBlocksRenderAsOneAnswerWithNumberedSources(t *testing.T) {
	cited := json.RawMessage(`{"type": "char_location", "cited_text": "apples,3\npears,5", "document_index": 0, "document_title": "smoke.csv"}`)
	run := int64(1)
	message := journal.Message{Seq: 2, RunID: &run, Role: journal.Assistant, Content: []json.RawMessage{
		text("The total is 8, since "),
		anthropictest.Text("apples are 3 and pears are 5", cited),
		text(" sum to that."),
	}}
	items, err := service.Items([]journal.Message{message}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Text != "The total is 8, since apples are 3 and pears are 5 [1] sum to that." {
		t.Fatalf("items %+v", items)
	}
	if !slices.Equal(items[0].Citations, []service.Citation{{Title: "smoke.csv", Cited: "apples,3\npears,5"}}) {
		t.Errorf("citations %+v", items[0].Citations)
	}
}

func TestDeleteThreadPurgesItAndItsSubagents(t *testing.T) {
	w := newWorld(t)
	w.push(reply(text("hi")), reply(text("child")))
	thread, _ := w.chat("hello", threads.Chat)
	child, err := service.CreateThread(t.Context(), w.d.Write, now0, threads.NewThread{Kind: threads.Subagent, ParentID: &thread})
	if err != nil {
		t.Fatal(err)
	}
	w.turn(child, "look", threads.Sub)
	if err := w.svc.DeleteThread(t.Context(), child); err != service.UnknownThread(child) {
		t.Errorf("deleting a subagent thread: %v", err)
	}
	if err := w.svc.DeleteThread(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{thread, child} {
		if found, _ := threads.Get(t.Context(), w.d.Read, id); found != nil {
			t.Errorf("thread %d survived", id)
		}
	}
	if err := w.svc.DeleteThread(t.Context(), thread); err != service.UnknownThread(thread) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestDeleteThreadIsRefusedWhileARunIsActive(t *testing.T) {
	w := newWorld(t)
	thread, _ := w.prepare("go", now0)
	if err := w.svc.DeleteThread(t.Context(), thread); err != service.RunActive("thread 1 has a run in progress") {
		t.Errorf("delete: %v", err)
	}
	if _, err := w.svc.Regenerate(t.Context(), thread); err != service.RunActive("thread 1 already has a run in progress") {
		t.Errorf("regenerate: %v", err)
	}
}

func TestRunViewCarriesCallsCostAndSkills(t *testing.T) {
	w := newWorld(t, probe("probe", tool.Read, echo))
	w.runner.ExpandSkills = func(_ context.Context, _ db.Querier, text string) (string, []string, error) {
		return strings.ReplaceAll(text, "/skill:brief", "Be brief."), []string{"brief"}, nil
	}
	w.push(toolTurn(use("tu1", "probe", nil)), reply(text("ok")))
	thread, run := w.chat("go /skill:brief", threads.Chat)
	if got := firstTexts(w.messages(thread))[0]; got != "go Be brief." {
		t.Errorf("journaled %q", got)
	}
	v, err := service.ViewRun(t.Context(), w.d.Read, run)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != run || len(v.Calls) != 1 || v.Cost <= 0 || !slices.Equal(v.Skills, []string{"brief"}) {
		t.Errorf("view %+v", v)
	}
	raw := mustJSON(t, v)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	for _, key := range []string{"id", "state", "calls", "cost", "skills"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("run_view JSON lacks %s", key)
		}
	}
}
