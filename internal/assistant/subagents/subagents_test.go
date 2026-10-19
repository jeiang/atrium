package subagents_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const now = 1_800_000_000.0

var expected = map[string][]string{
	"web":      {"recall", "web_fetch"},
	"mail":     {"contacts_lookup", "mail_read", "mail_search", "recall"},
	"notion":   {"fetch_knowledge", "recall", "search_knowledge"},
	"calendar": {"calendar_list", "recall"},
}

func setup(t *testing.T) (*db.DB, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	if err := reg.Add(registrytest.Load(t).Stubs(t)...); err != nil {
		t.Fatal(err)
	}
	return dbtest.Schema(t), reg
}

func names(tools []tool.Tool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = tl.Name
	}
	return out
}

func save(t *testing.T, d *db.DB, reg *registry.Registry, name, effort string, tools ...string) (*subagents.Agent, error) {
	t.Helper()
	return subagents.Save(t.Context(), d.Write, reg, subagents.Definition{
		Name: name, Description: "d", Prompt: "p", Effort: effort, Tools: tools, Enabled: true,
	}, now)
}

func TestBuiltinsAreSeededOnceAndEditable(t *testing.T) {
	d, reg := setup(t)
	ctx := t.Context()
	all, err := subagents.List(ctx, d.Read)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range all {
		got = append(got, a.Name)
		if !a.Builtin || !a.Enabled || a.Effort != "medium" {
			t.Errorf("%s = %+v", a.Name, a)
		}
	}
	if !slices.Equal(got, []string{"calendar", "mail", "notion", "observability", "web"}) {
		t.Errorf("names = %v", got)
	}
	if _, err := subagents.Save(ctx, d.Write, reg, subagents.Definition{
		Name: "web", Description: "Web, but careful", Prompt: "Search less.", Effort: "low", Tools: []string{"web_search", "web_fetch", "recall"}, Enabled: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, ""); err != nil {
		t.Fatal(err)
	}
	web, err := subagents.Get(ctx, d.Read, "web")
	if err != nil || web == nil {
		t.Fatalf("web: %v, %v", web, err)
	}
	if web.Prompt != "Search less." || web.Effort != "low" || !web.Builtin {
		t.Errorf("a reconnect reset an edited built-in: %+v", web)
	}
}

func TestRoleAllowlists(t *testing.T) {
	d, reg := setup(t)
	for role, want := range expected {
		a, err := subagents.Get(t.Context(), d.Read, role)
		if err != nil || a == nil {
			t.Fatalf("%s: %v", role, err)
		}
		got, err := reg.Run(t.Context(), tool.Sub, a.Tools, nil)
		if err != nil || !slices.Equal(names(got), want) {
			t.Errorf("%s: %v, %v; want %v", role, names(got), err, want)
		}
	}
}

func TestObservabilityAndWebSearchAllowlist(t *testing.T) {
	d, reg := setup(t)
	ctx := t.Context()
	obs, _ := subagents.Get(ctx, d.Read, "observability")
	got, err := reg.Run(ctx, tool.Sub, obs.Tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || !slices.Contains(names(got), "recall") {
		t.Fatalf("observability tools = %v", names(got))
	}
	for _, n := range names(got) {
		if n != "recall" && !strings.HasPrefix(n, "grafana_") {
			t.Errorf("observability has %s", n)
		}
	}
	web, _ := subagents.Get(ctx, d.Read, "web")
	mail, _ := subagents.Get(ctx, d.Read, "mail")
	if len(registry.ServerTools(tool.Sub, web.Tools)) != 1 || registry.ServerTools(tool.Sub, mail.Tools) != nil || registry.ServerTools(tool.Sub, nil) != nil {
		t.Error("web_search reaches a subagent only through its allowlist")
	}
}

func TestChildrenAreReadOnlyAndCannotSpawn(t *testing.T) {
	d, reg := setup(t)
	pool, err := reg.SubagentPool()
	if err != nil || len(pool) == 0 {
		t.Fatalf("pool: %v, %v", pool, err)
	}
	for _, tl := range pool {
		if tl.Effect != tool.Read {
			t.Errorf("%s is %s", tl.Name, tl.Effect)
		}
		if slices.Contains([]string{"research", "run_code", "notify", "schedule_create", "schedule_list", "load_skill", "context_status", "propose"}, tl.Name) {
			t.Errorf("%s is in the pool", tl.Name)
		}
	}
	all, _ := subagents.List(t.Context(), d.Read)
	for _, a := range all {
		tools, err := reg.Run(t.Context(), tool.Sub, a.Tools, nil)
		if err != nil || len(tools) == 0 {
			t.Fatalf("%s: %v, %v", a.Name, tools, err)
		}
		for _, tl := range tools {
			if tl.Effect != tool.Read || tl.Name == "research" {
				t.Errorf("%s offers %s (%s)", a.Name, tl.Name, tl.Effect)
			}
		}
	}
}

func TestCustomAgentCRUD(t *testing.T) {
	d, reg := setup(t)
	ctx := t.Context()
	made, err := save(t, d, reg, "finance", "high", "spending", "bills", "recall")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(made.Tools, []string{"bills", "recall", "spending"}) || made.Effort != "high" || made.Builtin || !made.Enabled {
		t.Errorf("made = %+v", made)
	}
	has := func() bool {
		enabled, err := subagents.Enabled(ctx, d.Read)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(enabled, func(a subagents.Agent) bool { return a.Name == "finance" })
	}
	if !has() {
		t.Error("finance is not enabled")
	}
	if ok, err := subagents.SetEnabled(ctx, d.Write, "finance", false, now); !ok || err != nil || has() {
		t.Errorf("disable: %v, %v", ok, err)
	}
	if ok, err := subagents.SetEnabled(ctx, d.Write, "nope", false, now); ok || err != nil {
		t.Errorf("unknown: %v, %v", ok, err)
	}
	if ok, err := subagents.Delete(ctx, d.Write, "finance"); !ok || err != nil {
		t.Errorf("delete: %v, %v", ok, err)
	}
	if got, _ := subagents.Get(ctx, d.Read, "finance"); got != nil {
		t.Error("finance survived its deletion")
	}
	if ok, err := subagents.Delete(ctx, d.Write, "finance"); ok || err != nil {
		t.Errorf("second delete: %v, %v", ok, err)
	}
	_, err = subagents.Delete(ctx, d.Write, "mail")
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("deleting a built-in: %v", err)
	}
	if _, ok := err.(subagents.Error); !ok {
		t.Errorf("%T is not subagents.Error", err)
	}
}

func TestSaveRejectsBadDefinitions(t *testing.T) {
	d, reg := setup(t)
	for _, c := range []struct {
		name, effort string
		tools        []string
	}{
		{"Bad Name", "medium", []string{"recall"}},
		{"ok", "extreme", []string{"recall"}},
		{"ok", "medium", nil},
		{"ok", "medium", []string{"notion_create_page"}},
		{"ok", "medium", []string{"notify"}},
		{"ok", "medium", []string{"research"}},
		{"ok", "medium", []string{"schedule_list"}},
		{"ok", "medium", []string{"no_such_tool"}},
		{"ok", "medium", []string{"  ", ""}},
	} {
		if _, err := save(t, d, reg, c.name, c.effort, c.tools...); err == nil {
			t.Errorf("%+v saved", c)
		} else if _, ok := err.(subagents.Error); !ok {
			t.Errorf("%+v: %T", c, err)
		}
	}
	if got, _ := subagents.Get(t.Context(), d.Read, "ok"); got != nil {
		t.Error("a rejected definition was stored")
	}
	for name, def := range map[string]subagents.Definition{
		"description": {Name: "ok", Description: strings.Repeat("d", 161), Prompt: "p", Effort: "low", Tools: []string{"recall"}},
		"empty":       {Name: "ok", Description: "  ", Prompt: "p", Effort: "low", Tools: []string{"recall"}},
		"prompt":      {Name: "ok", Description: "d", Prompt: strings.Repeat("p", 4001), Effort: "low", Tools: []string{"recall"}},
	} {
		if _, err := subagents.Save(t.Context(), d.Write, reg, def, now); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	_, err := save(t, d, reg, "ok", "medium", "recall", "web_search", " mail_read ")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := subagents.Get(t.Context(), d.Read, "ok")
	if !slices.Equal(got.Tools, []string{"mail_read", "recall", "web_search"}) {
		t.Errorf("tools = %v", got.Tools)
	}
}

func TestCardsAndDigest(t *testing.T) {
	d, _ := setup(t)
	ctx := t.Context()
	mail, _ := subagents.Get(ctx, d.Read, "mail")
	parent, err := threads.Create(ctx, d.Write, now, threads.NewThread{Kind: threads.Chat})
	if err != nil {
		t.Fatal(err)
	}
	prun, _ := journal.StartRun(ctx, d.Write, parent, "chat", "m", "medium", now, nil, nil)
	child, err := subagents.CreateChild(ctx, d.Write, parent, prun, *mail, "mail: task", now)
	if err != nil {
		t.Fatal(err)
	}
	thr, _ := threads.Get(ctx, d.Read, child)
	card, err := subagents.ChildCard(ctx, d.Read, *thr, now+5)
	if err != nil || card.Status != subagents.Queued || card.RunID != nil || *card.Elapsed != 5 || *card.ParentRunID != prun || *card.Role != "mail" {
		t.Fatalf("queued card = %+v, %v", card, err)
	}
	run, _ := journal.StartRun(ctx, d.Write, child, "sub", "m", "medium", now, &prun, nil)
	if _, err := d.Write.Exec("INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "+
		"VALUES (?, ?, ?, 'sub', 'm', 'medium', 111, 22, 0, 333, 44, 0, 'low', 0.0123)", now, child, run); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.StartToolCall(ctx, d.Write, run, "u1", "mail_search", []byte(`{"secret":"TOPSECRET"}`), journal.Safe, now); err != nil {
		t.Fatal(err)
	}
	card, err = subagents.ChildCard(ctx, d.Read, *thr, now+7)
	if err != nil || card.Status != subagents.Running || *card.Current != "mail_search" || *card.Elapsed != 7 ||
		card.Input != 111 || card.Output != 44 || card.CacheRead != 333 || card.CacheWrite != 22 || card.Cost != 0.0123 {
		t.Fatalf("running card = %+v, %v", card, err)
	}
	if err := journal.FinishRun(ctx, d.Write, run, journal.Failed, now+9, new("boom")); err != nil {
		t.Fatal(err)
	}
	card, _ = subagents.ChildCard(ctx, d.Read, *thr, now+20)
	if card.Status != subagents.ErrorStatus || *card.Error != "boom" || *card.Elapsed != 9 || card.Current != nil {
		t.Errorf("failed card = %+v", card)
	}
	if err := journal.SetRunState(ctx, d.Write, run, journal.Interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("UPDATE runs SET ended_at = NULL WHERE id = ?", run); err != nil {
		t.Fatal(err)
	}
	card, _ = subagents.ChildCard(ctx, d.Read, *thr, now+20)
	if card.Status != subagents.Interrupted || card.Elapsed != nil {
		t.Errorf("interrupted card = %+v", card)
	}

	if got, _ := subagents.DigestOf(ctx, d.Read, child); got != "" {
		t.Errorf("digest before any answer = %q", got)
	}
	long := strings.Repeat("é", 7000)
	for _, text := range []string{"first", "  " + long + "  ", ""} {
		blocks := [][]byte{[]byte(`{"type":"thinking","thinking":"hm","signature":"s"}`), mustText(text)}
		if _, err := journal.AppendMessage(ctx, d, child, &run, journal.Assistant, rawBlocks(blocks), now, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := subagents.DigestOf(ctx, d.Read, child); got != strings.Repeat("é", 6000) {
		t.Errorf("digest is %d runes, want the last answer with text clipped to 6000", len([]rune(got)))
	}
}

func TestSystemAddendum(t *testing.T) {
	got := subagents.SystemAddendum(subagents.Agent{Name: "mail", Prompt: "Look."})
	if !strings.HasPrefix(got, "You are a read-only research subagent") || !strings.HasSuffix(got, "\n\nAgent `mail`: Look.") {
		t.Errorf("addendum = %q", got)
	}
}
