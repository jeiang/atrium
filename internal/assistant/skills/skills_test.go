package skills_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
)

const now = 1_800_000_000.0

func seed(t *testing.T, d *db.DB, bodies ...string) {
	t.Helper()
	for i := 0; i+1 < len(bodies); i += 2 {
		if err := skills.Save(t.Context(), d.Write, bodies[i], "About "+bodies[i], bodies[i+1], now); err != nil {
			t.Fatal(err)
		}
	}
}

func names(list []skills.Skill) []string {
	out := []string{}
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) || (got == nil) != (want == nil) && len(want) == 0 {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func TestReferencesParseBothFormsInOrderWithoutDuplicates(t *testing.T) {
	text := "Start with /skill:alpha, then [[skill:beta]] and /skill:alpha again; https://x.test/skill:nope and foo/skill:bar stay."
	eq(t, "references", skills.References(text), []string{"alpha", "beta"})
	eq(t, "bad name", skills.References("/skill:Bad"), []string{})
	eq(t, "empty", skills.References(""), []string{})
	eq(t, "word before", skills.References("x/skill:a é/skill:b _/skill:c (/skill:d"), []string{"d"})
	eq(t, "long name", skills.References("/skill:"+strings.Repeat("a", 49)), []string{strings.Repeat("a", 48)})
}

func TestReferencesOfAndReferencedByFlagUnknownNames(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	seed(t, d, "a", "see /skill:b and /skill:ghost and /skill:a", "b", "plain")
	refs, err := skills.ReferencesOf(ctx, d.Read, "a")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "a known", refs.Known, []string{"b"})
	eq(t, "a unknown", refs.Unknown, []string{"ghost"})
	refs, _ = skills.ReferencesOf(ctx, d.Read, "b")
	eq(t, "b known", refs.Known, []string{})
	eq(t, "b unknown", refs.Unknown, []string{})
	by, _ := skills.ReferencedBy(ctx, d.Read, "b")
	eq(t, "b referenced by", by, []string{"a"})
	by, _ = skills.ReferencedBy(ctx, d.Read, "a")
	eq(t, "a referenced by", by, []string{})
	all, _ := skills.List(ctx, d.Read, "")
	listed, err := skills.WithReferences(ctx, d.Read, all)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Name != "a" || !slices.Equal(listed[0].References.Unknown, []string{"ghost"}) || listed[1].Name != "b" || !slices.Equal(listed[1].ReferencedBy, []string{"a"}) {
		t.Errorf("listed = %+v", listed[:2])
	}
}

func TestClosureTerminatesOnCyclesAndAppliesCaps(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	seed(t, d, "a", "go /skill:b", "b", "back to /skill:a and on to [[skill:c]]", "c", "leaf", "big", strings.Repeat("x", 500))
	run := func(names []string, maxSkills, maxChars int) (got, unknown, skipped []string) {
		chosen, unknown, skipped, err := skills.Closure(ctx, d.Read, names, maxSkills, maxChars)
		if err != nil {
			t.Fatal(err)
		}
		return nameList(chosen), unknown, skipped
	}
	got, unknown, skipped := run([]string{"a"}, 5, 20000)
	eq(t, "chosen", got, []string{"a", "b", "c"})
	eq(t, "unknown", unknown, []string{})
	eq(t, "skipped", skipped, []string{})
	got, unknown, skipped = run([]string{"a", "nope"}, 2, 20000)
	eq(t, "capped chosen", got, []string{"a", "b"})
	eq(t, "capped unknown", unknown, []string{"nope"})
	eq(t, "capped skipped", skipped, []string{"c"})
	got, _, skipped = run([]string{"c", "big", "a"}, 5, 20)
	eq(t, "chars chosen", got, []string{"c", "a"})
	eq(t, "chars skipped", skipped, []string{"big", "b"})
}

func nameList(s []skills.Skill) []string { return names(s) }

func TestExpandAppendsBlocksTransitivelyAndNotesUnknownAndCapped(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	seed(t, d, "a", "Alpha steps /skill:b", "b", "Beta steps", "c", "Gamma")
	text := "Please run /skill:a and /skill:zzz today"
	out, used, notes, err := skills.Expand(ctx, d.Read, text)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, text+"\n\n") || !strings.Contains(out, "<skill name=\"a\">\nAlpha steps /skill:b\n</skill>") ||
		!strings.Contains(out, "<skill name=\"b\">\nBeta steps\n</skill>") || strings.Contains(out, `<skill name="c"`) {
		t.Errorf("expanded = %q", out)
	}
	eq(t, "used", used, []string{"a", "b"})
	eq(t, "notes", notes, []string{"Unknown skills (no such skill is defined): zzz"})
	if !strings.Contains(out, notes[0]) {
		t.Error("the note is not in the text")
	}
	out, used, notes, _ = skills.Expand(ctx, d.Read, "no tokens here")
	if out != "no tokens here" || len(used) != 0 || len(notes) != 0 {
		t.Errorf("no tokens: %q %v %v", out, used, notes)
	}
	var more []string
	for _, n := range []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6"} {
		more = append(more, n, "body "+n)
	}
	seed(t, d, more...)
	_, used, notes, _ = skills.Expand(ctx, d.Read, "/skill:s0 /skill:s1 /skill:s2 /skill:s3 /skill:s4 /skill:s5 /skill:s6")
	eq(t, "capped used", used, []string{"s0", "s1", "s2", "s3", "s4"})
	if len(notes) != 1 || !strings.Contains(notes[0], "use load_skill): s5, s6") {
		t.Errorf("notes = %v", notes)
	}
	hooked, hookUsed, err := skills.ExpandHook(ctx, d.Read, "/skill:b")
	if err != nil || !strings.Contains(hooked, "Beta steps") || !slices.Equal(hookUsed, []string{"b"}) {
		t.Errorf("hook: %q %v %v", hooked, hookUsed, err)
	}
}

func TestSaveValidatesAndNormalises(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	for _, c := range []struct{ name, description, body string }{
		{"Bad Name", "d", "b"},
		{"-lead", "d", "b"},
		{"ok", "", "b"},
		{"ok", strings.Repeat("d", 161), "b"},
		{"ok", "d", "  "},
		{"ok", "d", strings.Repeat("b", 20001)},
	} {
		err := skills.Save(ctx, d.Write, c.name, c.description, c.body, now)
		if _, ok := err.(skills.Error); !ok {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if err := skills.Save(ctx, d.Write, " ok ", "two\n  lines ", "  body  ", now); err != nil {
		t.Fatal(err)
	}
	got, _ := skills.Get(ctx, d.Read, "ok")
	if got == nil || got.Description != "two lines" || got.Body != "body" || got.UpdatedAt != now {
		t.Errorf("saved = %+v", got)
	}
	if err := skills.Save(ctx, d.Write, "ok", "new", "newer", now+1); err != nil {
		t.Fatal(err)
	}
	got, _ = skills.Get(ctx, d.Read, "ok")
	if got.Description != "new" || got.Body != "newer" {
		t.Errorf("replaced = %+v", got)
	}
	if ok, err := skills.Delete(ctx, d.Write, "ok"); !ok || err != nil {
		t.Errorf("delete: %v %v", ok, err)
	}
	if ok, _ := skills.Delete(ctx, d.Write, "ok"); ok {
		t.Error("deleted twice")
	}
	if got, _ := skills.Get(ctx, d.Read, "ok"); got != nil {
		t.Error("skill survived its deletion")
	}
}

func TestSearchExcludesNonMatching(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	for _, s := range [][3]string{{"weekly-review", "Review the week", "check the calendar"}, {"trip-plan", "Plan trips", "book flights"}} {
		if err := skills.Save(ctx, d.Write, s[0], s[1], s[2], now); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string][]string{"FLIGHTS": {"trip-plan"}, "review the": {"weekly-review"}, "zzz": {}, "TRIP": {"trip-plan"}} {
		got, err := skills.List(ctx, d.Read, q)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "search "+q, names(got), want)
	}
	all, _ := skills.List(ctx, d.Read, "")
	eq(t, "all", names(all), []string{"trip-plan", "weekly-review", "wiki-capture", "wiki-ingest", "wiki-query", "wiki-review"})
}

func TestBuiltinsAreSeededOnceAndEditable(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	all, _ := skills.List(ctx, d.Read, "")
	eq(t, "built-ins", names(all), []string{"wiki-capture", "wiki-ingest", "wiki-query", "wiki-review"})
	if err := skills.Save(ctx, d.Write, "wiki-query", "Mine", "My steps", now); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := skills.Get(ctx, d.Read, "wiki-query")
	if got.Body != "My steps" {
		t.Errorf("a reconnect reset an edited built-in: %q", got.Body)
	}
	block, err := skills.IndexBlock(ctx, d.Read)
	if err != nil || !strings.HasPrefix(block, "Skills (operator-written playbooks; call load_skill(name) to read one when it applies):\n- wiki-capture: ") ||
		!strings.Contains(block, "\n- wiki-query: Mine\n") {
		t.Errorf("index = %q, %v", block, err)
	}
	for _, n := range []string{"wiki-capture", "wiki-ingest", "wiki-query", "wiki-review"} {
		if _, err := skills.Delete(ctx, d.Write, n); err != nil {
			t.Fatal(err)
		}
	}
	if block, _ := skills.IndexBlock(ctx, d.Read); block != "" {
		t.Errorf("index with no skills = %q", block)
	}
}

func TestLoadedInThreadAndRelated(t *testing.T) {
	d := dbtest.Schema(t)
	ctx := t.Context()
	seed(t, d, "a", "A body /skill:b /skill:ghost /skill:a", "b", "B body [[skill:a]]")
	thread := insertThread(t, d)
	run := insertRun(t, d, thread, "")
	loaded, err := skills.LoadedInThread(ctx, d.Read, thread)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("loaded = %v, %v", loaded, err)
	}
	related, unknown, err := skills.RelatedTo(ctx, d.Read, "a", map[string]bool{"a": true})
	if err != nil || len(related) != 1 || related[0].Name != "b" || related[0].Description != "About b" {
		t.Fatalf("related = %+v, %v", related, err)
	}
	eq(t, "unknown", unknown, []string{"ghost"})
	for _, c := range []struct {
		id, name, state string
	}{{"u1", "a", journal.OK}, {"u2", "zz", journal.Error}} {
		call, err := journal.StartToolCall(ctx, d.Write, run, c.id, "load_skill", []byte(`{"name":"`+c.name+`"}`), journal.Safe, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.FinishToolCall(ctx, d.Write, call, c.state, now, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	loaded, _ = skills.LoadedInThread(ctx, d.Read, thread)
	if len(loaded) != 1 || !loaded["a"] {
		t.Errorf("after load_skill: %v", loaded)
	}
	related, _, _ = skills.RelatedTo(ctx, d.Read, "b", loaded)
	if len(related) != 0 {
		t.Errorf("b still points back to a: %+v", related)
	}
	other := insertThread(t, d)
	insertRun(t, d, other, `["b"]`)
	loaded, _ = skills.LoadedInThread(ctx, d.Read, other)
	if len(loaded) != 1 || !loaded["b"] {
		t.Errorf("expanded skills: %v", loaded)
	}
	related, _, _ = skills.RelatedTo(ctx, d.Read, "a", loaded)
	if len(related) != 0 {
		t.Errorf("a points to the expanded b: %+v", related)
	}
}

func insertThread(t *testing.T, d *db.DB) int64 {
	t.Helper()
	res, err := d.Write.Exec("INSERT INTO threads (kind, effort, created_at, updated_at) VALUES ('chat', 'medium', ?, ?)", now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertRun(t *testing.T, d *db.DB, thread int64, skillsJSON string) int64 {
	t.Helper()
	var sj any
	if skillsJSON != "" {
		sj = skillsJSON
	}
	res, err := d.Write.Exec("INSERT INTO runs (thread_id, kind, model, effort, state, started_at, skills_json) VALUES (?, 'chat', 'm', 'medium', 'done', ?, ?)", thread, now, sj)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
