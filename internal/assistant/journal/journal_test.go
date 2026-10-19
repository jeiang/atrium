package journal_test

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func ptr[T any](v T) *T { return &v }

func raw(blocks ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		out[i] = json.RawMessage(b)
	}
	return out
}

func world(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d := dbtest.Schema(t)
	id, err := threads.Create(t.Context(), d.Write, 1, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	return d, id
}

func start(t *testing.T, d *db.DB, thread int64, now float64, owner string) int64 {
	t.Helper()
	id, err := journal.StartRun(t.Context(), d.Write, thread, "chat", "claude-haiku-5-5", "medium", now, nil, &owner)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func run(t *testing.T, d *db.DB, id int64) journal.Run {
	t.Helper()
	r, err := journal.GetRun(t.Context(), d.Read, id)
	if err != nil || r == nil {
		t.Fatalf("GetRun(%d) = %v, %v", id, r, err)
	}
	return *r
}

func TestRunLifecycle(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	id := start(t, d, thread, 100, "w1")
	r := run(t, d, id)
	if r.State != journal.Running || r.StartedAt != 100 || *r.LeaseUntil != 100+journal.LeaseSeconds || *r.Owner != "w1" || r.Steps != 0 || r.Superseded || r.StopRequested {
		t.Errorf("new run %+v", r)
	}
	if err := journal.BumpSteps(ctx, d.Write, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := journal.RequestStop(ctx, d.Write, id); err != nil || !ok {
		t.Fatalf("RequestStop = %v, %v", ok, err)
	}
	if err := journal.FinishRun(ctx, d.Write, id, journal.Failed, 150, ptr("boom")); err != nil {
		t.Fatal(err)
	}
	if ok, err := journal.RequestStop(ctx, d.Write, id); err != nil || ok {
		t.Errorf("stopping a finished run = %v, %v", ok, err)
	}
	r = run(t, d, id)
	if r.State != journal.Failed || *r.Error != "boom" || *r.EndedAt != 150 || r.Steps != 1 || !r.StopRequested {
		t.Errorf("finished run %+v", r)
	}
	if got, err := journal.GetRun(ctx, d.Read, 999); err != nil || got != nil {
		t.Errorf("GetRun(unknown) = %v, %v", got, err)
	}
	all, err := journal.RunsOf(ctx, d.Read, thread)
	if err != nil || len(all) != 1 || all[0].ID != id {
		t.Errorf("RunsOf = %+v, %v", all, err)
	}
	if active, err := journal.ActiveRun(ctx, d.Read, thread); err != nil || active != nil {
		t.Errorf("ActiveRun of a finished thread = %v, %v", active, err)
	}
	if err := journal.SetRunState(ctx, d.Write, id, journal.Interrupted); err != nil {
		t.Fatal(err)
	}
	if active, err := journal.ActiveRun(ctx, d.Read, thread); err != nil || active == nil || active.ID != id {
		t.Errorf("an interrupted run is active: %v, %v", active, err)
	}
}

func TestLeasesInterruptOnlyLapsedRunsAndResumeIsAtomic(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	live := start(t, d, thread, 1000, "w1")
	lapsed := start(t, d, thread, 1000-journal.LeaseSeconds-10, "w2")
	count, err := journal.MarkInterrupted(ctx, d.Write, 1001)
	if err != nil || count != 1 {
		t.Fatalf("MarkInterrupted = %d, %v", count, err)
	}
	if r := run(t, d, live); r.State != journal.Running {
		t.Errorf("a live lease was interrupted: %+v", r)
	}
	if r := run(t, d, lapsed); r.State != journal.Interrupted || r.Owner != nil || r.LeaseUntil != nil {
		t.Errorf("lapsed run %+v", r)
	}
	if got, err := journal.InterruptedRuns(ctx, d.Read); err != nil || len(got) != 1 || got[0].ID != lapsed {
		t.Errorf("InterruptedRuns = %+v, %v", got, err)
	}
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := journal.ClaimResume(ctx, d.Write, lapsed, "claimer-"+string(rune('a'+i)), 2000)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d processes claimed one run", wins)
	}
	owner := *run(t, d, lapsed).Owner
	if ok, err := journal.RenewLease(ctx, d.Write, lapsed, "someone-else", 3000); err != nil || ok {
		t.Errorf("a stranger renewed the lease: %v, %v", ok, err)
	}
	if ok, err := journal.RenewLease(ctx, d.Write, lapsed, owner, 3000); err != nil || !ok {
		t.Errorf("owner could not renew: %v, %v", ok, err)
	}
	if r := run(t, d, lapsed); *r.LeaseUntil != 3000+journal.LeaseSeconds {
		t.Errorf("lease %v", *r.LeaseUntil)
	}
	if ok, err := journal.ClaimResume(ctx, d.Write, live, "x", 1); err != nil || ok {
		t.Errorf("claimed a running run: %v, %v", ok, err)
	}
}

func TestOwnerIsUniquePerCall(t *testing.T) {
	a, b := journal.NewOwner(), journal.NewOwner()
	if a == b || strings.Count(a, ":") < 2 {
		t.Errorf("owners %q %q", a, b)
	}
}

func TestLimitsAndSkills(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	id := start(t, d, thread, 0, "w")
	if got, err := journal.LimitsOf(ctx, d.Read, id); err != nil || got != (journal.Limits{}) {
		t.Fatalf("limits of a fresh run = %+v, %v", got, err)
	}
	l := journal.Limits{MaxSteps: ptr(int64(3)), SystemExtra: ptr("BE BRIEF é")}
	if err := journal.SetLimits(ctx, d.Write, id, l); err != nil {
		t.Fatal(err)
	}
	got, err := journal.LimitsOf(ctx, d.Read, id)
	if err != nil || *got.MaxSteps != 3 || got.MaxCostUSD != nil || *got.SystemExtra != "BE BRIEF é" {
		t.Errorf("limits %+v, %v", got, err)
	}
	if text := *run(t, d, id).LimitsJSON; text != `{"max_steps": 3, "system_extra": "BE BRIEF \u00e9"}` {
		t.Errorf("limits_json = %s", text)
	}
	if err := journal.SetLimits(ctx, d.Write, id, journal.Limits{MaxCostUSD: ptr(1.0)}); err != nil {
		t.Fatal(err)
	}
	if text := *run(t, d, id).LimitsJSON; text != `{"max_cost_usd": 1.0}` {
		t.Errorf("limits_json = %s", text)
	}
	if got, err := journal.LimitsOf(ctx, d.Read, 999); err != nil || got != (journal.Limits{}) {
		t.Errorf("limits of no run = %+v, %v", got, err)
	}
	if names, err := journal.SkillsOf(run(t, d, id)); err != nil || len(names) != 0 {
		t.Errorf("skills of a fresh run = %v, %v", names, err)
	}
	if err := journal.SetSkills(ctx, d.Write, id, []string{"wiki-capture", "bills"}); err != nil {
		t.Fatal(err)
	}
	if names, err := journal.SkillsOf(run(t, d, id)); err != nil || !slices.Equal(names, []string{"wiki-capture", "bills"}) {
		t.Errorf("skills %v, %v", names, err)
	}
	if text := *run(t, d, id).SkillsJSON; text != `["wiki-capture", "bills"]` {
		t.Errorf("skills_json = %s", text)
	}
}

func TestMessagesKeepTheRawBlocks(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	runID := start(t, d, thread, 1, "w")
	blocks := raw(
		`{"type":"thinking","thinking":"hm é","signature":"sig=="}`,
		`{ "type" : "text", "text" : "Hello\nworld" }`,
		`{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q","n":1.0}}`,
		`{"type":"text","text":"  second  "}`,
	)
	seq, err := journal.AppendMessage(ctx, d, thread, &runID, journal.Assistant, blocks, 5, ptr("sig1"))
	if err != nil || seq != 1 {
		t.Fatalf("AppendMessage = %d, %v", seq, err)
	}
	seq, err = journal.AppendMessage(ctx, d, thread, &runID, journal.User, raw(`{"type":"tool_result","tool_use_id":"tu","content":"ok"}`), 6, nil)
	if err != nil || seq != 2 {
		t.Fatalf("second AppendMessage = %d, %v", seq, err)
	}
	var stored string
	if err := d.Read.QueryRowContext(ctx, "SELECT content_json FROM thread_messages WHERE seq = 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"thinking","thinking":"hm é","signature":"sig=="},{"type":"text","text":"Hello\nworld"},` +
		`{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"q","n":1.0}},{"type":"text","text":"  second  "}]`
	if stored != want {
		t.Errorf("content_json\n got %s\nwant %s", stored, want)
	}
	msgs, err := journal.MessagesOf(ctx, d.Read, thread, false)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("MessagesOf = %+v, %v", msgs, err)
	}
	if m := msgs[0]; m.Seq != 1 || m.Role != journal.Assistant || *m.SystemSig != "sig1" || *m.RunID != runID || m.CreatedAt != 5 || m.Superseded || len(m.Content) != 4 {
		t.Errorf("first message %+v", m)
	}
	var thinking struct{ Thinking, Signature string }
	if err := json.Unmarshal(msgs[0].Content[0], &thinking); err != nil || thinking.Signature != "sig==" {
		t.Errorf("thinking block %s", msgs[0].Content[0])
	}
	last, err := journal.LastMessage(ctx, d.Read, thread)
	if err != nil || last == nil || last.Seq != 2 {
		t.Errorf("LastMessage = %+v, %v", last, err)
	}
	var fts []string
	rows, err := d.Read.QueryContext(ctx, "SELECT text FROM thread_fts WHERE thread_id = ?", thread)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		fts = append(fts, s)
	}
	_ = rows.Close()
	if !slices.Equal(fts, []string{"Hello\nworld\n  second"}) {
		t.Errorf("indexed text %q", fts)
	}
	var updated float64
	if err := d.Read.QueryRowContext(ctx, "SELECT updated_at FROM threads WHERE id = ?", thread).Scan(&updated); err != nil || updated != 6 {
		t.Errorf("thread updated_at %v, %v", updated, err)
	}
	if _, err := journal.AppendMessage(ctx, d, thread, nil, journal.User, raw(`"not an object"`), 7, nil); err == nil {
		t.Error("a non-object block was journaled")
	}
	if got, err := journal.LastMessage(ctx, d.Read, 999); err != nil || got != nil {
		t.Errorf("LastMessage(empty) = %v, %v", got, err)
	}
}

func TestParallelAppendsGetDistinctSeqs(t *testing.T) {
	d, thread := world(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := journal.AppendMessage(t.Context(), d, thread, nil, journal.User, raw(`{"type":"text","text":"x"}`), 1, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	msgs, err := journal.MessagesOf(t.Context(), d.Read, thread, false)
	if err != nil || len(msgs) != 10 {
		t.Fatalf("%d messages, %v", len(msgs), err)
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Errorf("message %d has seq %d", i, m.Seq)
		}
	}
}

func TestLastOperatorMessageAndSupersede(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	r1 := start(t, d, thread, 1, "w")
	r2 := start(t, d, thread, 2, "w")
	add := func(runID int64, role string, blocks ...string) {
		t.Helper()
		if _, err := journal.AppendMessage(ctx, d, thread, &runID, role, raw(blocks...), 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	add(r1, journal.User, `{"type":"text","text":"first"}`)
	add(r1, journal.Assistant, `{"type":"text","text":"a1"}`)
	add(r2, journal.User, `{"type":"text","text":"second"}`)
	add(r2, journal.Assistant, `{"type":"tool_use","id":"t","name":"n","input":{}}`)
	add(r2, journal.User, `{"type":"tool_result","tool_use_id":"t","content":"ok"}`)
	op, err := journal.LastOperatorMessage(ctx, d.Read, thread)
	if err != nil || op == nil || op.Seq != 3 {
		t.Fatalf("LastOperatorMessage = %+v, %v", op, err)
	}
	if _, err := d.Write.ExecContext(ctx, "INSERT INTO context_projections (thread_id, ts, kind, base_seq, head_json) VALUES (?, 0, 'fold', 4, '[]')", thread); err != nil {
		t.Fatal(err)
	}
	if err := journal.Supersede(ctx, d, thread, 3, true); err != nil {
		t.Fatal(err)
	}
	msgs, err := journal.MessagesOf(ctx, d.Read, thread, false)
	if err != nil || len(msgs) != 3 || msgs[2].Seq != 3 {
		t.Fatalf("after Supersede(keep) %+v, %v", msgs, err)
	}
	everything, err := journal.MessagesOf(ctx, d.Read, thread, true)
	if err != nil || len(everything) != 5 || !everything[3].Superseded {
		t.Errorf("superseded messages are kept for audit: %+v, %v", everything, err)
	}
	if run(t, d, r1).Superseded || !run(t, d, r2).Superseded {
		t.Error("only the runs from the message's run on are superseded")
	}
	var projections int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM context_projections WHERE thread_id = ?", thread).Scan(&projections); err != nil || projections != 0 {
		t.Errorf("projections %d, %v", projections, err)
	}
	if err := journal.Supersede(ctx, d, thread, 1, false); err != nil {
		t.Fatal(err)
	}
	if got, err := journal.MessagesOf(ctx, d.Read, thread, false); err != nil || len(got) != 0 {
		t.Errorf("after Supersede(1) %+v, %v", got, err)
	}
	if op, err := journal.LastOperatorMessage(ctx, d.Read, thread); err != nil || op != nil {
		t.Errorf("LastOperatorMessage of a retired conversation = %+v, %v", op, err)
	}
}

func TestQueueGuardsTheRunEnd(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	if id, queued, err := journal.QueueMessage(ctx, d, thread, "early", 1, nil); err != nil || queued || id != 0 {
		t.Fatalf("queueing with no run = %d, %v, %v", id, queued, err)
	}
	runID := start(t, d, thread, 1, "w")
	files := raw(`{"type":"document","title":"é"}`)
	id, queued, err := journal.QueueMessage(ctx, d, thread, "later", 2, files)
	if err != nil || !queued || id == 0 {
		t.Fatalf("QueueMessage = %d, %v, %v", id, queued, err)
	}
	if _, _, err := journal.QueueMessage(ctx, d, thread, "no files", 3, nil); err != nil {
		t.Fatal(err)
	}
	open, err := journal.Queued(ctx, d.Read, thread)
	if err != nil || len(open) != 2 || open[0].Text != "later" || len(open[0].Files) != 1 || len(open[1].Files) != 0 || *open[1].FilesJSON != "[]" {
		t.Fatalf("Queued = %+v, %v", open, err)
	}
	if string(open[0].Files[0]) != `{"type":"document","title":"é"}` || *open[0].FilesJSON != `[{"type":"document","title":"é"}]` {
		t.Errorf("files %s / %s", open[0].Files[0], *open[0].FilesJSON)
	}
	if done, err := journal.FinishUnlessQueued(ctx, d, runID, thread, journal.Done, 9); err != nil || done {
		t.Errorf("a run finished with messages queued: %v, %v", done, err)
	}
	if r := run(t, d, runID); r.State != journal.Running {
		t.Errorf("run %+v", r)
	}
	taken, err := journal.TakeQueued(ctx, d.Write, thread, runID, 10)
	if err != nil || len(taken) != 2 {
		t.Fatalf("TakeQueued = %+v, %v", taken, err)
	}
	if open, err := journal.Queued(ctx, d.Read, thread); err != nil || len(open) != 0 {
		t.Errorf("Queued after take = %+v, %v", open, err)
	}
	if done, err := journal.FinishUnlessQueued(ctx, d, runID, thread, journal.Done, 11); err != nil || !done {
		t.Errorf("FinishUnlessQueued = %v, %v", done, err)
	}
	if r := run(t, d, runID); r.State != journal.Done || *r.EndedAt != 11 {
		t.Errorf("run %+v", r)
	}
	if _, queued, err := journal.QueueMessage(ctx, d, thread, "after", 12, nil); err != nil || queued {
		t.Errorf("queued on a finished run: %v, %v", queued, err)
	}
	interrupted := start(t, d, thread, 13, "w")
	if err := journal.SetRunState(ctx, d.Write, interrupted, journal.Interrupted); err != nil {
		t.Fatal(err)
	}
	if _, queued, err := journal.QueueMessage(ctx, d, thread, "x", 14, nil); err != nil || queued {
		t.Errorf("queued on an interrupted run: %v, %v", queued, err)
	}
}

func TestToolCalls(t *testing.T) {
	d, thread := world(t)
	ctx := t.Context()
	runID := start(t, d, thread, 1, "w")
	args := json.RawMessage(`{ "q": "caf\u00e9", "n": 1.0, "nested": {"b": [1, 2], "a": null} }`)
	callID, err := journal.StartToolCall(ctx, d.Write, runID, "toolu_1", "mail_search", args, journal.Safe, 2)
	if err != nil {
		t.Fatal(err)
	}
	call, err := journal.GetToolCall(ctx, d.Read, runID, "toolu_1")
	if err != nil || call == nil {
		t.Fatalf("GetToolCall = %v, %v", call, err)
	}
	wantArgs := `{"q":"café","n":1.0,"nested":{"b":[1,2],"a":null}}`
	if call.ArgsJSON != wantArgs || call.State != journal.Started || call.ReplayClass != journal.Safe || call.ID != callID || call.FinishedAt != nil {
		t.Errorf("call %+v", call)
	}
	if want := "9c204e0e13d4b94219beb95e02d6ff31b3e430b7294d0b02aaf23f1df5e940d4"; call.ArgsSHA256 != want {
		t.Errorf("sha256 %s", call.ArgsSHA256)
	}
	if err := journal.FinishToolCall(ctx, d.Write, callID, journal.Error, 3, ptr("nope"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.StartToolCall(ctx, d.Write, runID, "toolu_1", "x", json.RawMessage(`{}`), journal.Write, 4); err == nil {
		t.Error("a duplicate tool_use id was journaled")
	}
	other := start(t, d, thread, 5, "w")
	if _, err := journal.StartToolCall(ctx, d.Write, other, "toolu_1", "x", json.RawMessage(`{}`), journal.Write, 6); err != nil {
		t.Fatal(err)
	}
	call, _ = journal.GetToolCall(ctx, d.Read, runID, "toolu_1")
	if call.State != journal.Error || *call.Error != "nope" || *call.FinishedAt != 3 {
		t.Errorf("finished call %+v", call)
	}
	if none, err := journal.GetToolCall(ctx, d.Read, runID, "missing"); err != nil || none != nil {
		t.Errorf("GetToolCall(missing) = %v, %v", none, err)
	}
	if got, err := journal.ToolCallsOf(ctx, d.Read, runID); err != nil || len(got) != 1 {
		t.Errorf("ToolCallsOf = %+v, %v", got, err)
	}
	all, err := journal.ToolCallsForThread(ctx, d.Read, thread)
	if err != nil || len(all) != 2 || all[journal.CallKey{RunID: other, ToolUseID: "toolu_1"}].ReplayClass != journal.Write {
		t.Errorf("ToolCallsForThread = %+v, %v", all, err)
	}
}
