package main

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
)

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSkillsExportImportRoundTripAndIndexBudget(t *testing.T) {
	flags := flagsExcept("assistant", "true")
	dir, api := moduleFixture(t, flags)

	source := filepath.Join(dir, "source", "trip-plan")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	card := "---\nname: trip-plan\ndescription: >\n  Plan trips: flights, \"hotels\"\n  and packing\nlicense: MIT\n---\n\n# Trip plan\n\nBook flights first. See /skill:wiki-query.\n"
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	if ran := runApp("skills", "import", source); ran.code != cli.ExitOK || ran.stdout != "created trip-plan\n" {
		t.Fatalf("import: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}

	first := filepath.Join(dir, "export-1")
	if ran := runApp("skills", "export", first); ran.code != cli.ExitOK {
		t.Fatalf("export: exit %d stderr %q", ran.code, ran.stderr)
	}
	tree := readTree(t, first)
	want := "---\nname: trip-plan\ndescription: \"Plan trips: flights, \\\"hotels\\\" and packing\"\n---\n\n# Trip plan\n\nBook flights first. See /skill:wiki-query.\n"
	if tree[filepath.Join("trip-plan", "SKILL.md")] != want || len(tree) != 5 {
		t.Fatalf("exported tree %v", tree)
	}

	t.Setenv("ATRIUM_DB", filepath.Join(dir, "second.db"))
	if ran := runApp("skills", "import", first); ran.code != cli.ExitOK || !strings.Contains(ran.stdout, "created trip-plan\n") || !strings.Contains(ran.stdout, "updated wiki-capture\n") {
		t.Fatalf("round-trip import: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	second := filepath.Join(dir, "export-2")
	if ran := runApp("skills", "export", second); ran.code != cli.ExitOK {
		t.Fatalf("second export: exit %d stderr %q", ran.code, ran.stderr)
	}
	again := readTree(t, second)
	if len(again) != len(tree) {
		t.Fatalf("round trip changed the skill set: %d then %d", len(tree), len(again))
	}
	for name, text := range tree {
		if again[name] != text {
			t.Errorf("%s changed in the round trip", name)
		}
	}

	if ran := runApp("skills", "export", "--name", "ghost", filepath.Join(dir, "none")); ran.code != cli.ExitError || !strings.Contains(ran.stderr, "no skill named ghost") {
		t.Errorf("unknown export: exit %d stderr %q", ran.code, ran.stderr)
	}
	bad := filepath.Join(dir, "bad", "Upper")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ran := runApp("skills", "import", filepath.Join(dir, "bad")); ran.code != cli.ExitError || !strings.Contains(ran.stderr, "Upper/SKILL.md: name must be lowercase") {
		t.Errorf("invalid import: exit %d stderr %q", ran.code, ran.stderr)
	}

	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	prompt := func() string {
		t.Helper()
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("hi")))
		ran := runApp("agent", "run", "--text", "hello", "--kind", "chat")
		requests := api.Requests()
		if ran.code != cli.ExitOK || len(requests) == 0 {
			t.Fatalf("agent run: exit %d stderr %q", ran.code, ran.stderr)
		}
		return string(requests[len(requests)-1].Body)
	}
	full := prompt()
	if !strings.Contains(full, "- trip-plan: Plan trips") || strings.Contains(full, "more skills are not listed") {
		t.Fatalf("index under budget should list every skill")
	}
	if ran := runApp("settings", "set", "agent_skill_index_chars", "300"); ran.code != cli.ExitOK {
		t.Fatalf("set budget: %q", ran.stderr)
	}
	cut := prompt()
	if strings.Contains(cut, "- trip-plan: Plan trips") || !strings.Contains(cut, "more skills are not listed because of the skills index budget") || !strings.Contains(cut, "- wiki-") {
		t.Fatalf("index over budget should keep built-in skills and cut the rest")
	}

	base := startServeIn(t, dir)
	_, page, _ := fetch(t, http.DefaultClient, "GET", base+"/settings/skills", nil)
	for _, fragment := range []string{"Over the 300-character skills index budget", "Not indexed", `href="/settings/skills/export"`, `action="/settings/skills/import"`} {
		if !strings.Contains(page, fragment) {
			t.Errorf("skills page lacks %q", fragment)
		}
	}
}
