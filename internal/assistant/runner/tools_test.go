package runner_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const tz = "America/Port_of_Spain"

var noon = func() float64 {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return float64(time.Date(2026, 10, 15, 12, 0, 0, 0, loc).Unix())
}()

type world struct {
	t   *testing.T
	d   *db.DB
	srv *anthropictest.Server
	svc *service.Service
	env config.Env
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := dbtest.Schema(t)
	dir := t.TempDir()
	env := config.Map(map[string]string{
		"ATRIUM_DB": filepath.Join(dir, "atrium.db"), "ATRIUM_CACHE_DIR": filepath.Join(dir, "cache"), "ATRIUM_MODE": "shadow", "ATRIUM_TZ": tz,
	})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := anthropictest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc := service.New(ctx, d, cfg, env, r)
	svc.Client = srv.Client()
	t.Cleanup(func() {
		svc.Close()
		cancel()
	})
	tick := noon
	svc.Now = func() float64 { tick += 0.001; return tick }
	return &world{t: t, d: d, srv: srv, svc: svc, env: env}
}

func (w *world) thread(kind string) int64 {
	w.t.Helper()
	id, err := service.CreateThread(w.t.Context(), w.d.Write, w.svc.Now(), threads.NewThread{Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) turn(thread int64, kind, text string, script ...anthropictest.Response) int64 {
	w.t.Helper()
	w.srv.Push(script...)
	id, err := w.svc.RunOnce(w.t.Context(), thread, text, service.Start{Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	if w.srv.Remaining() != 0 {
		w.t.Fatalf("%d scripted replies unused", w.srv.Remaining())
	}
	return id
}

func (w *world) request(i int) map[string]any {
	w.t.Helper()
	reqs := w.srv.Requests()
	if i >= len(reqs) {
		w.t.Fatalf("request %d of %d", i, len(reqs))
	}
	return reqs[i].JSON(w.t)
}

func resultOf(t *testing.T, body map[string]any, toolUseID string) map[string]any {
	t.Helper()
	for _, m := range body["messages"].([]any) {
		content, _ := m.(map[string]any)["content"].([]any)
		for _, b := range content {
			if block := b.(map[string]any); block["type"] == "tool_result" && block["tool_use_id"] == toolUseID {
				return block
			}
		}
	}
	t.Fatalf("no tool_result for %s", toolUseID)
	return nil
}

func decoded(t *testing.T, block map[string]any) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(block["content"].(string)), &v); err != nil {
		t.Fatalf("%v: %v", block["content"], err)
	}
	return v
}

func isError(t *testing.T, block map[string]any, want string) {
	t.Helper()
	if block["is_error"] != true || !strings.Contains(block["content"].(string), want) {
		t.Errorf("result %v, want an error containing %q", block, want)
	}
}

func use(id, name string, input map[string]any) json.RawMessage {
	return anthropictest.ToolUse(id, name, input)
}

func toolTurn(blocks ...json.RawMessage) anthropictest.Response {
	return anthropictest.Reply("tool_use", blocks...)
}

func reply(text string) anthropictest.Response {
	return anthropictest.Reply("end_turn", anthropictest.Text(text))
}

func names(tools []tool.Tool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = tl.Name
	}
	slices.Sort(out)
	return out
}

func TestRunKindsOfferThePythonMatrix(t *testing.T) {
	reg, err := toolset.Registry()
	if err != nil {
		t.Fatal(err)
	}
	ours := []string{"context_drop", "context_fold", "context_pin", "context_status", "load_skill", "plan", "profile_get", "profile_set", "propose",
		"recall", "schedule_cancel", "schedule_create", "schedule_list", "schedule_update"}
	want := map[tool.Kind][]string{
		tool.Chat:    ours,
		tool.Cron:    {"context_drop", "context_fold", "context_pin", "context_status", "load_skill", "plan", "profile_get", "propose", "recall"},
		tool.Nightly: {"context_drop", "context_fold", "context_pin", "context_status", "load_skill", "plan", "profile_get", "profile_set", "recall"},
		tool.Sub:     {"recall"},
	}
	for kind, expected := range want {
		offered, err := reg.For(kind)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range names(offered) {
			if slices.Contains(ours, n) {
				got = append(got, n)
			}
		}
		if !slices.Equal(got, expected) {
			t.Errorf("%s offers %v, want %v", kind, got, expected)
		}
	}
}

func TestDropIsStagedThenCommittedAtTheNextTurnAndStaysRecallable(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	medium := strings.Repeat("m", 1500)
	if err := skills.Save(ctx, w.d.Write, "long", "A long one", medium, noon); err != nil {
		t.Fatal(err)
	}
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "first",
		toolTurn(anthropictest.Thinking("a", "sa"), use("tu1", "load_skill", map[string]any{"name": "long"})),
		toolTurn(anthropictest.Thinking("b", "sb"), use("tu2", "context_status", nil)),
		toolTurn(use("tu3", "context_drop", map[string]any{"ids": []string{"tu1"}})),
		reply("noted"),
	)
	dashboard := decoded(t, resultOf(t, w.request(2), "tu2"))
	var item map[string]any
	for _, it := range dashboard["items"].([]any) {
		if it.(map[string]any)["id"] == "tu1" {
			item = it.(map[string]any)
		}
	}
	if item == nil || item["tool"] != "load_skill" || item["state"] != "active" || item["seq"] != float64(3) {
		t.Fatalf("dashboard item %v", item)
	}
	if dashboard["approx_tokens"].(float64) <= item["tokens"].(float64) || dashboard["turn_start_seq"] != float64(1) {
		t.Errorf("dashboard %v", dashboard)
	}
	staged := decoded(t, resultOf(t, w.request(3), "tu3"))
	if ids, _ := staged["staged_drop"].([]any); len(ids) != 1 || ids[0] != "tu1" || staged["staged_savings"].(float64) <= 0 {
		t.Errorf("staged %v", staged)
	}
	if !strings.Contains(resultOf(t, w.request(3), "tu1")["content"].(string), medium) {
		t.Error("the staged result left the context before the commit")
	}
	if state := ledgerState(t, w.d, thread, "tu1"); state != actx.StagedDrop {
		t.Errorf("ledger state %q", state)
	}

	w.turn(thread, threads.Chat, "second",
		toolTurn(use("tu4", "recall", map[string]any{"result_id": 1})),
		reply("ok"),
	)
	first := w.request(4)
	if got := decoded(t, resultOf(t, first, "tu1")); got["result_id"] != float64(1) || got["dropped"] != true {
		t.Errorf("committed stub %v", got)
	}
	paged := decoded(t, resultOf(t, w.request(5), "tu4"))
	if !strings.Contains(paged["text"].(string), medium) || paged["offset"] != float64(0) || paged["next_offset"] != nil || paged["total_chars"].(float64) < 1500 {
		t.Errorf("recall %v", paged)
	}
	if state := ledgerState(t, w.d, thread, "tu1"); state != actx.Dropped {
		t.Errorf("ledger state %q", state)
	}

	w.turn(thread, threads.Chat, "third",
		toolTurn(use("tu5", "recall", map[string]any{"result_id": 1, "offset": 1498, "limit": 1})),
		toolTurn(use("tu6", "recall", map[string]any{"result_id": 99})),
		toolTurn(use("tu7", "recall", map[string]any{"result_id": "1"})),
		reply("done"),
	)
	page := decoded(t, resultOf(t, w.request(7), "tu5"))
	if page["text"] != "m" || page["next_offset"] != float64(1499) {
		t.Errorf("recall page %v", page)
	}
	isError(t, resultOf(t, w.request(8), "tu6"), "no stored result 99 in this thread")
	isError(t, resultOf(t, w.request(9), "tu7"), "result_id must be an integer")
}

func ledgerState(t *testing.T, d *db.DB, thread int64, item string) string {
	t.Helper()
	l, err := actx.LedgerRows(t.Context(), d.Read, thread)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := l.Get(item)
	if !ok {
		return ""
	}
	return row.State
}

func TestPinBlocksDropAndFoldReplacesCompletedTurns(t *testing.T) {
	w := newWorld(t)
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "one",
		toolTurn(use("p1", "profile_get", nil)),
		reply("answer one"),
	)
	w.turn(thread, threads.Chat, "two",
		toolTurn(use("pin", "context_pin", map[string]any{"ids": []string{"p1"}})),
		toolTurn(use("drop", "context_drop", map[string]any{"ids": []string{"p1"}})),
		toolTurn(use("bad", "context_fold", map[string]any{"from_seq": 2, "to_seq": 3, "summary": "x"})),
		toolTurn(use("fold", "context_fold", map[string]any{"from_seq": 1, "to_seq": 4, "summary": "Turn one: the profile was empty.", "source_ids": []any{"mail:1", 7}})),
		toolTurn(use("many", "context_drop", map[string]any{"ids": make([]string, 51)})),
		toolTurn(use("none", "context_pin", map[string]any{"ids": []string{}})),
		reply("folded"),
	)
	if pinned := decoded(t, resultOf(t, w.request(3), "pin")); !slices.Equal(pinned["pinned"].([]any), []any{"p1"}) {
		t.Errorf("pin %v", pinned)
	}
	isError(t, resultOf(t, w.request(4), "drop"), "pinned")
	isError(t, resultOf(t, w.request(5), "bad"), "operator message")
	isError(t, resultOf(t, w.request(6), "fold"), "pinned")
	isError(t, resultOf(t, w.request(7), "many"), "at most 50 ids per call")
	isError(t, resultOf(t, w.request(8), "none"), "ids must be a non-empty list")

	w.turn(thread, threads.Chat, "three",
		toolTurn(use("fold2", "context_fold", map[string]any{"from_seq": 5, "to_seq": 18, "summary": "Turn two: pinned p1, folds tried.", "source_ids": []string{"mail:2"}})),
		toolTurn(use("long", "context_fold", map[string]any{"from_seq": 5, "to_seq": 18, "summary": strings.Repeat("s", 4001)})),
		reply("ok"),
	)
	staged := decoded(t, resultOf(t, w.request(10), "fold2"))
	if staged["staged_fold"] != "fold:5-18" || staged["tokens_saved"].(float64) <= 0 {
		t.Errorf("fold %v", staged)
	}
	isError(t, resultOf(t, w.request(11), "long"), "summary is limited to 4000 characters")

	w.turn(thread, threads.Chat, "four", reply("fin"))
	sent := w.request(12)["messages"].([]any)
	var roles []string
	for _, m := range sent {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if !slices.Equal(roles, []string{"user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user"}) {
		t.Fatalf("roles %v", roles)
	}
	foldText := sent[4].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(foldText, "[context fold of seq 5-18] Turn two") || !strings.Contains(foldText, "mail:2") {
		t.Errorf("fold text %q", foldText)
	}
	manifest, err := actx.Manifest(t.Context(), w.d.Read, thread)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "Fold seq 5-18") || !strings.Contains(manifest, "Pinned: p1") {
		t.Errorf("manifest %q", manifest)
	}
}

func TestPlanToolSetsTheThreadChecklist(t *testing.T) {
	w := newWorld(t)
	thread := w.thread(threads.Chat)
	run := w.turn(thread, threads.Chat, "plan it",
		toolTurn(use("pl1", "plan", map[string]any{"items": []any{map[string]any{"text": "  Read mail "}, map[string]any{"text": "Reply", "done": true}}})),
		toolTurn(use("pl2", "plan", map[string]any{"items": []any{map[string]any{"text": " "}}})),
		toolTurn(use("pl3", "plan", map[string]any{"items": "no"})),
		reply("planned"),
	)
	got := decoded(t, resultOf(t, w.request(1), "pl1"))
	items := got["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["text"] != "Read mail" || items[0].(map[string]any)["done"] != false || got["done"] != float64(1) || got["total"] != float64(2) {
		t.Errorf("plan %v", got)
	}
	isError(t, resultOf(t, w.request(2), "pl2"), "each step needs a non-empty text")
	isError(t, resultOf(t, w.request(3), "pl3"), "items must be a list of at most 20 steps")
	var stored string
	if err := w.d.Read.QueryRowContext(t.Context(), "SELECT items_json FROM thread_plans WHERE thread_id = ? AND run_id = ?", thread, run).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `[{"text": "Read mail", "done": false}, {"text": "Reply", "done": true}]` {
		t.Errorf("stored %s", stored)
	}
	var events int
	if err := w.d.Read.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM run_events WHERE run_id = ? AND kind = 'plan'", run).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("%d plan events", events)
	}
}

func TestProfileSetIsLoggedAndShownAsASystemBlock(t *testing.T) {
	w := newWorld(t)
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "remember",
		toolTurn(use("s1", "profile_set", map[string]any{"key": "tz", "value": "AST"})),
		toolTurn(use("s2", "profile_set", map[string]any{"key": "tz", "value": "UTC"})),
		toolTurn(use("s3", "profile_set", map[string]any{"key": strings.Repeat("k", 65), "value": "x"})),
		toolTurn(use("s4", "profile_set", map[string]any{"key": "city"})),
		toolTurn(use("g1", "profile_get", map[string]any{"key": "tz"})),
		toolTurn(use("g2", "profile_get", map[string]any{"key": "nope"})),
		reply("saved"),
	)
	first := decoded(t, resultOf(t, w.request(1), "s1"))
	second := decoded(t, resultOf(t, w.request(2), "s2"))
	if first["previous"] != nil || first["write_id"] != float64(1) || second["previous"] != "AST" || second["value"] != "UTC" || second["write_id"] != float64(2) {
		t.Errorf("profile_set %v %v", first, second)
	}
	isError(t, resultOf(t, w.request(3), "s3"), "key is limited to 64 characters and value to 500")
	isError(t, resultOf(t, w.request(4), "s4"), "value is required")
	if got := decoded(t, resultOf(t, w.request(5), "g1")); len(got["entries"].([]any)) != 1 || got["entries"].([]any)[0].(map[string]any)["value"] != "UTC" {
		t.Errorf("profile_get %v", got)
	}
	if got := decoded(t, resultOf(t, w.request(6), "g2")); len(got["entries"].([]any)) != 0 {
		t.Errorf("profile_get %v", got)
	}
	system := w.request(0)["system"].([]any)
	for _, b := range system {
		if strings.Contains(b.(map[string]any)["text"].(string), "Operator profile") {
			t.Fatal("an empty profile was sent as a block")
		}
	}
	w.turn(thread, threads.Chat, "again", reply("hi"))
	system = w.request(7)["system"].([]any)
	if len(system) < 2 || !strings.Contains(system[1].(map[string]any)["text"].(string), "Operator profile (preferences recorded by the operator or the assistant):\n- tz: UTC") {
		t.Errorf("system %v", system)
	}
	rows, err := w.d.Read.QueryContext(t.Context(), "SELECT tool, target, before_json, after_json, undo_json FROM agent_writes ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ tool, target, before, after, undo string }
	writes, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.tool, &x.target, &x.before, &x.after, &x.undo)
		return x, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"profile_set", "profile:tz", `{"value": null}`, `{"value": "AST"}`, `{"applied": "AST", "key": "tz", "op": "profile_restore", "previous": null}`},
		{"profile_set", "profile:tz", `{"value": "AST"}`, `{"value": "UTC"}`, `{"applied": "UTC", "key": "tz", "op": "profile_restore", "previous": "AST"}`},
	}
	if !slices.Equal(writes, want) {
		t.Errorf("agent_writes %v", writes)
	}
	w.turn(w.thread(threads.Cron), threads.Cron, "cron", toolTurn(use("c1", "profile_set", map[string]any{"key": "a", "value": "b"})), reply("no"))
	isError(t, resultOf(t, w.request(9), "c1"), "unknown tool profile_set")
}

func TestProposeRejectsUnknownKindsUntilAKindRegisters(t *testing.T) {
	w := newWorld(t)
	thread := w.thread(threads.Cron)
	w.turn(thread, threads.Cron, "propose",
		toolTurn(use("pr1", "propose", map[string]any{"kind": "notion_edit", "payload": map[string]any{}})),
		toolTurn(use("pr2", "propose", map[string]any{"kind": "send_email", "payload": "x"})),
		toolTurn(use("pr3", "propose", map[string]any{"kind": "", "payload": map[string]any{}})),
		reply("nothing"),
	)
	isError(t, resultOf(t, w.request(1), "pr1"), "unknown proposal kind 'notion_edit'; allowed: ")
	isError(t, resultOf(t, w.request(2), "pr2"), "unknown proposal kind 'send_email'; allowed: ")
	isError(t, resultOf(t, w.request(3), "pr3"), "kind is required")
}

func TestScheduleToolsFlow(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	if err := scheduler.EnsureNightly(ctx, w.d.Write, tz, noon); err != nil {
		t.Fatal(err)
	}
	if err := skills.Save(ctx, w.d.Write, "digest", "About digest", "Digest steps", noon); err != nil {
		t.Fatal(err)
	}
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "schedule",
		toolTurn(use("c1", "schedule_create", map[string]any{"name": "weekly", "prompt": "Review.", "schedule": "weekly fri 16:00"})),
		toolTurn(use("c2", "schedule_create", map[string]any{"name": "x", "prompt": "p", "schedule": "*/10 * * * *"})),
		toolTurn(use("c3", "schedule_create", map[string]any{"name": "Daily", "prompt": "Run /skill:digest then /skill:nope", "schedule": "daily 08:00"})),
		toolTurn(use("u1", "schedule_update", map[string]any{"id": 2, "schedule": "daily 08:00"})),
		toolTurn(use("u2", "schedule_update", map[string]any{"id": 3, "prompt": "Run /skill:digest"})),
		toolTurn(use("u3", "schedule_update", map[string]any{"id": 3, "prompt": "No skills", "enabled": false})),
		toolTurn(use("u4", "schedule_update", map[string]any{"id": 1, "enabled": false})),
		toolTurn(use("u5", "schedule_update", map[string]any{"id": 2, "enabled": "yes"})),
		toolTurn(use("u6", "schedule_update", map[string]any{"id": "2"})),
		toolTurn(use("l1", "schedule_list", nil)),
		toolTurn(use("x1", "schedule_cancel", map[string]any{"id": 2})),
		toolTurn(use("l2", "schedule_list", nil)),
		toolTurn(use("x2", "schedule_cancel", map[string]any{"id": 2})),
		toolTurn(use("x3", "schedule_cancel", map[string]any{"id": 1})),
		reply("scheduled"),
	)
	made := decoded(t, resultOf(t, w.request(1), "c1"))
	if made["id"] != float64(2) || made["created_by"] != "agent" || !strings.HasPrefix(made["next_due"].(string), "2026-10-16T16:00") || made["enabled"] != true || made["skills"] != nil {
		t.Errorf("created %v", made)
	}
	var inThread int64
	if err := w.d.Read.QueryRowContext(ctx, "SELECT created_in_thread FROM jobs WHERE id = 2").Scan(&inThread); err != nil || inThread != thread {
		t.Errorf("created_in_thread %d, %v", inThread, err)
	}
	isError(t, resultOf(t, w.request(2), "c2"), "at least")
	daily := decoded(t, resultOf(t, w.request(3), "c3"))
	if !slices.Equal(daily["skills"].([]any), []any{"digest"}) || !strings.Contains(daily["note"].(string), "nope") {
		t.Errorf("skill tokens %v", daily)
	}
	if got := decoded(t, resultOf(t, w.request(4), "u1")); got["schedule"] != "daily 08:00" {
		t.Errorf("update %v", got)
	}
	if got := decoded(t, resultOf(t, w.request(5), "u2")); !slices.Equal(got["skills"].([]any), []any{"digest"}) || got["note"] != nil {
		t.Errorf("update %v", got)
	}
	if got := decoded(t, resultOf(t, w.request(6), "u3")); got["skills"] != nil || got["enabled"] != false {
		t.Errorf("update %v", got)
	}
	isError(t, resultOf(t, w.request(7), "u4"), "built-in runs are managed by the operator")
	isError(t, resultOf(t, w.request(8), "u5"), "enabled must be a boolean")
	isError(t, resultOf(t, w.request(9), "u6"), "id must be an integer")
	if got := decoded(t, resultOf(t, w.request(10), "l1")); len(got["schedules"].([]any)) != 2 {
		t.Errorf("list %v", got)
	}
	if got := decoded(t, resultOf(t, w.request(11), "x1")); got["cancelled"] != float64(2) {
		t.Errorf("cancel %v", got)
	}
	if got := decoded(t, resultOf(t, w.request(12), "l2")); len(got["schedules"].([]any)) != 1 || got["schedules"].([]any)[0].(map[string]any)["id"] != float64(3) {
		t.Errorf("list %v", got)
	}
	isError(t, resultOf(t, w.request(13), "x2"), "no active schedule 2")
	isError(t, resultOf(t, w.request(14), "x3"), "built-in runs are managed by the operator")
	cron := w.thread(threads.Cron)
	w.turn(cron, threads.Cron, "cron", toolTurn(use("c9", "schedule_list", nil)), reply("no"))
	isError(t, resultOf(t, w.request(16), "c9"), "unknown tool schedule_list")
}

func TestLoadSkillListsUnloadedReferencesAndHandlesCycles(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	for name, body := range map[string]string{"a": "A body /skill:b /skill:ghost /skill:a", "b": "B body [[skill:a]]"} {
		if err := skills.Save(ctx, w.d.Write, name, "About "+name, body, noon); err != nil {
			t.Fatal(err)
		}
	}
	thread := w.thread(threads.Chat)
	w.turn(thread, threads.Chat, "load",
		toolTurn(use("k1", "load_skill", map[string]any{"name": "a"})),
		toolTurn(use("k2", "load_skill", map[string]any{"name": "b"})),
		toolTurn(use("k3", "load_skill", map[string]any{"name": "ghost"})),
		toolTurn(use("k4", "load_skill", map[string]any{"name": ""})),
		reply("loaded"),
	)
	out := decoded(t, resultOf(t, w.request(1), "k1"))
	body := out["body"].(string)
	if !strings.HasPrefix(body, "A body") || !slices.Equal(out["references"].([]any), []any{"b"}) || out["description"] != "About a" {
		t.Errorf("load_skill a: %v", out)
	}
	if !strings.Contains(body, "- b: About b") || !strings.Contains(body, "do not exist: ghost") || strings.Contains(body, "- a:") {
		t.Errorf("body %q", body)
	}
	out = decoded(t, resultOf(t, w.request(2), "k2"))
	if len(out["references"].([]any)) != 0 || strings.Contains(out["body"].(string), "not yet loaded") {
		t.Errorf("load_skill b after a: %v", out)
	}
	isError(t, resultOf(t, w.request(3), "k3"), "no skill named 'ghost'")
	isError(t, resultOf(t, w.request(4), "k4"), "name is required")

	other := w.thread(threads.Chat)
	w.turn(other, threads.Chat, "Use /skill:b",
		toolTurn(use("k5", "load_skill", map[string]any{"name": "a"})),
		reply("ok"),
	)
	if out := decoded(t, resultOf(t, w.request(6), "k5")); len(out["references"].([]any)) != 0 {
		t.Errorf("an expanded skill counts as loaded: %v", out)
	}
	got, err := journal.GetRun(ctx, w.d.Read, 2)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if used, _ := journal.SkillsOf(*got); !slices.Equal(used, []string{"b", "a"}) {
		t.Errorf("expanded skills %v", used)
	}
}
