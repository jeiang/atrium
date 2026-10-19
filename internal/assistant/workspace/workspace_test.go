package workspace_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

func ptr[T any](v T) *T { return &v }

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPlan(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	thread, err := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := workspace.PlanOf(ctx, d.Read, thread); err != nil || got != nil {
		t.Fatalf("PlanOf(none) = %v, %v", got, err)
	}
	items := decode(t, `[{"text": "  Read the bill é ", "done": 1}, {"text": "Pay it"}, {"text": "`+strings.Repeat("x", 300)+`", "done": "yes"}]`)
	cleaned, err := workspace.SetPlan(ctx, d.Write, thread, nil, items, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 3 || cleaned[0] != (workspace.PlanItem{Text: "Read the bill é", Done: true}) || cleaned[1].Done || len(cleaned[2].Text) != 200 || !cleaned[2].Done {
		t.Errorf("cleaned %+v", cleaned)
	}
	var stored string
	if err := d.Read.QueryRowContext(ctx, "SELECT items_json FROM thread_plans WHERE thread_id = ?", thread).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, `[{"text": "Read the bill é", "done": true}, {"text": "Pay it", "done": false}, {"text": "xxx`) {
		t.Errorf("items_json = %.100s", stored)
	}
	runID := int64(7)
	if _, err := d.Write.ExecContext(ctx, "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (7, ?, 'chat', 'm', 'low', 'running', 0)", thread); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SetPlan(ctx, d.Write, thread, &runID, decode(t, `[{"text": "only"}]`), 9); err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.PlanOf(ctx, d.Read, thread)
	if err != nil || plan == nil || len(plan.Items) != 1 || plan.UpdatedAt != 9 || *plan.RunID != 7 {
		t.Errorf("plan %+v, %v", plan, err)
	}
	if _, err := workspace.SetPlan(ctx, d.Write, thread, nil, decode(t, `[]`), 10); err != nil {
		t.Errorf("an empty plan: %v", err)
	}
}

func TestCleanPlanRefusals(t *testing.T) {
	many := make([]any, 21)
	for i := range many {
		many[i] = map[string]any{"text": "x"}
	}
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "items must be a list of at most 20 steps"},
		{"steps", "items must be a list of at most 20 steps"},
		{many, "items must be a list of at most 20 steps"},
		{decode(t, `["text"]`), "each step needs a non-empty text"},
		{decode(t, `[{"text": "  "}]`), "each step needs a non-empty text"},
		{decode(t, `[{"text": 5}]`), "each step needs a non-empty text"},
		{decode(t, `[{}]`), "each step needs a non-empty text"},
	} {
		_, err := workspace.CleanPlan(c.in)
		var pe workspace.PlanError
		if !errors.As(err, &pe) || err.Error() != c.want {
			t.Errorf("CleanPlan(%v) = %v, want %q", c.in, err, c.want)
		}
	}
	if got, err := workspace.CleanPlan(many[:20]); err != nil || len(got) != 20 {
		t.Errorf("20 steps: %v, %v", len(got), err)
	}
}

func TestFilesAndChildren(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	thread, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat})
	other, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO thread_files (id, thread_id, name, media_type, bytes, path, origin, created_at, text, tokens) VALUES (2, ?, 'notes.txt', 'text/plain', 22, 'state/files/2/2-notes.txt', 'upload', 5, 'Remember.', 3)", []any{thread}},
		{"INSERT INTO thread_files (id, thread_id, name, media_type, bytes, path, origin, created_at, anthropic_file_id, project_id) VALUES (1, ?, 'a.png', 'image/png', 9, 'p', 'tool', 4, 'file_1', 8)", []any{thread}},
		{"INSERT INTO thread_files (id, thread_id, name, media_type, bytes, path, origin, created_at) VALUES (3, ?, 'b', 'x', 0, 'q', 'upload', 1)", []any{other}},
	} {
		if _, err := d.Write.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	files, err := workspace.FilesOf(ctx, d.Read, thread)
	if err != nil || len(files) != 2 || files[0].ID != 1 || *files[0].AnthropicFileID != "file_1" || *files[0].ProjectID != 8 || files[0].Text != nil ||
		files[1].ID != 2 || *files[1].Text != "Remember." || files[1].Tokens != 3 {
		t.Fatalf("FilesOf = %+v, %v", files, err)
	}
	if f, err := workspace.GetFile(ctx, d.Read, thread, 2); err != nil || f == nil || f.Name != "notes.txt" {
		t.Errorf("GetFile = %+v, %v", f, err)
	}
	if f, err := workspace.GetFile(ctx, d.Read, thread, 3); err != nil || f != nil {
		t.Errorf("another thread's file: %+v, %v", f, err)
	}
	c1, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Subagent, ParentID: &thread})
	c2, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Subagent, ParentID: &thread})
	gone, _ := threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Subagent, ParentID: &thread})
	if err := threads.Purge(ctx, d.Write, gone, 9); err != nil {
		t.Fatal(err)
	}
	kids, err := workspace.ChildThreads(ctx, d.Read, thread)
	if err != nil || len(kids) != 2 || kids[0].ID != c1 || kids[1].ID != c2 {
		t.Errorf("ChildThreads = %+v, %v", kids, err)
	}
	if kids, err := workspace.ChildThreads(ctx, d.Read, other); err != nil || kids == nil || len(kids) != 0 {
		t.Errorf("no children: %v, %v", kids, err)
	}
}

type sourcesWorld struct {
	d        *db.DB
	thread   int64
	child    int64
	run      int64
	childRun int64
}

func newSourcesWorld(t *testing.T) *sourcesWorld {
	t.Helper()
	d := dbtest.Schema(t)
	ctx := t.Context()
	w := &sourcesWorld{d: d}
	w.thread, _ = threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Chat})
	w.child, _ = threads.Create(ctx, d.Write, 1, threads.NewThread{Kind: threads.Subagent, ParentID: &w.thread})
	if _, err := d.Write.ExecContext(ctx, "UPDATE threads SET role = 'web' WHERE id = ?", w.child); err != nil {
		t.Fatal(err)
	}
	owner := "w"
	w.run, _ = journal.StartRun(ctx, d.Write, w.thread, "chat", "m", "low", 1, nil, &owner)
	w.childRun, _ = journal.StartRun(ctx, d.Write, w.child, "sub", "m", "low", 1, &w.run, &owner)
	return w
}

func (w *sourcesWorld) call(t *testing.T, run int64, id, name, args, state string, result *string, journaled bool) {
	t.Helper()
	ctx := t.Context()
	callID, err := journal.StartToolCall(ctx, w.d.Write, run, id, name, json.RawMessage(args), journal.Safe, 1)
	if err != nil {
		t.Fatal(err)
	}
	var resultID *int64
	if result != nil && !journaled {
		res, err := w.d.Write.ExecContext(ctx, "INSERT INTO tool_results (run_id, tool_call_id, tool, tokens, bytes, sha256, body, created_at) VALUES (?, ?, ?, 1, 1, 'x', ?, 1)", run, callID, name, *result)
		if err != nil {
			t.Fatal(err)
		}
		rid, _ := res.LastInsertId()
		resultID = &rid
	}
	if err := journal.FinishToolCall(ctx, w.d.Write, callID, state, 2, nil, resultID); err != nil {
		t.Fatal(err)
	}
	if result != nil && journaled {
		thread := w.thread
		if run == w.childRun {
			thread = w.child
		}
		block, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": id, "content": *result})
		if _, err := journal.AppendMessage(ctx, w.d, thread, &run, journal.User, []json.RawMessage{block}, 3, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSources(t *testing.T) {
	w := newSourcesWorld(t)
	ctx := t.Context()
	w.call(t, w.run, "t1", workspace.WebFetch, `{"url":"https://a.test/x"}`, journal.OK, ptr(`{"url":"https://a.test/final","title":"Page A"}`), true)
	w.call(t, w.run, "t2", workspace.MailRead, `{"message_id":148}`, journal.OK, ptr(`{"headers":{"Subject":"Dentist"}}`), false)
	w.call(t, w.run, "t3", workspace.MailRead, `{"message_id":9}`, journal.OK, ptr(`not json`), true)
	w.call(t, w.run, "t4", workspace.FetchKnowledge, `{"page_id":"p1"}`, journal.OK, ptr(`{"page_id":"p1-real","title":"Alpha plan"}`), true)
	w.call(t, w.run, "t5", workspace.WebFetch, `{"url":"https://failed.test"}`, journal.Error, nil, false)
	w.call(t, w.run, "t6", "mail_search", `{"q":"x"}`, journal.OK, nil, false)
	w.call(t, w.run, "t7", workspace.WebFetch, `{"url":"https://a.test/x"}`, journal.OK, ptr(`{"url":"https://a.test/final"}`), true)
	w.call(t, w.childRun, "c1", workspace.WebFetch, `{"url":"https://child.test"}`, journal.OK, ptr(`{}`), true)
	w.call(t, w.run, "t8", workspace.MailRead, `{"message_id":"12"}`, journal.OK, ptr(`{}`), true)
	search := `{"type":"web_search_tool_result","tool_use_id":"srv1","content":[{"type":"web_search_result","url":"https://hit.test/1","title":"Hit one"},{"url":"https://hit.test/2"},{"title":"no url"},{"url":"https://a.test/final","title":"dup"}]}`
	if _, err := journal.AppendMessage(ctx, w.d, w.thread, &w.run, journal.Assistant, []json.RawMessage{json.RawMessage(search)}, 4, nil); err != nil {
		t.Fatal(err)
	}
	childSearch := `{"type":"web_search_tool_result","tool_use_id":"srv2","content":[{"url":"https://hit.test/child","title":"Child hit"}]}`
	if _, err := journal.AppendMessage(ctx, w.d, w.child, &w.childRun, journal.Assistant, []json.RawMessage{json.RawMessage(childSearch), json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srv3","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}}`)}, 4, nil); err != nil {
		t.Fatal(err)
	}
	got, err := workspace.SourcesOf(ctx, w.d.Read, w.thread)
	if err != nil {
		t.Fatal(err)
	}
	web := "web"
	want := []workspace.Source{
		{Kind: "web", Key: "https://a.test/final", Title: "Page A", URL: "https://a.test/final"},
		{Kind: "mail", Key: "m148", Title: "Dentist", URL: "/messages/148", MessageID: ptr(int64(148))},
		{Kind: "mail", Key: "m9", Title: "Message 9", URL: "/messages/9", MessageID: ptr(int64(9))},
		{Kind: "notion", Key: "np1-real", Title: "Alpha plan", URL: "/notion/p1-real", PageID: ptr("p1-real")},
		{Kind: "web", Key: "https://child.test", Title: "https://child.test", URL: "https://child.test", Via: &web},
		{Kind: "web", Key: "https://hit.test/1", Title: "Hit one", URL: "https://hit.test/1"},
		{Kind: "web", Key: "https://hit.test/2", Title: "https://hit.test/2", URL: "https://hit.test/2"},
		{Kind: "web", Key: "https://hit.test/child", Title: "Child hit", URL: "https://hit.test/child", Via: &web},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("SourcesOf\n got %s\nwant %s", gotJSON, wantJSON)
	}
	alone, err := workspace.SourcesOf(ctx, w.d.Read, w.child)
	if err != nil || !slices.ContainsFunc(alone, func(s workspace.Source) bool { return s.Key == "https://child.test" && s.Via == nil }) {
		t.Errorf("the child's own sources %+v, %v", alone, err)
	}
}

func TestSourcesOfAnEmptyThread(t *testing.T) {
	d := dbtest.Schema(t)
	thread, _ := threads.Create(t.Context(), d.Write, 1, threads.NewThread{Kind: threads.Chat})
	got, err := workspace.SourcesOf(t.Context(), d.Read, thread)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("SourcesOf = %v, %v", got, err)
	}
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Errorf("encodes as %s", b)
	}
}
