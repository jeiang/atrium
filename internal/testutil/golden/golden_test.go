package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mustDiff(t *testing.T, want, got string) []string {
	t.Helper()
	diffs, err := Diff([]byte(want), []byte(got))
	if err != nil {
		t.Fatal(err)
	}
	return diffs
}

func TestEqualIgnoresSpelling(t *testing.T) {
	cases := []struct{ name, want, got string }{
		{"key order", `{"b":1,"a":{"d":[1,2],"c":null}}`, `{"a":{"c":null,"d":[1,2]},"b":1}`},
		{"whitespace", "{\n  \"a\": [\n    true\n  ]\n}\n", `{"a":[true]}`},
		{"html escaping", `{"t":"<b>a & b</b>"}`, `{"t":"\u003cb\u003ea \u0026 b\u003c/b\u003e"}`},
		{"unicode escaping", `{"t":"é – “x”"}`, `{"t":"\u00e9 \u2013 \u201cx\u201d"}`},
		{"line separators", `{"t":"a` + "\u2028" + `b"}`, `{"t":"a\u2028b"}`},
		{"integer as float", `{"n":1.0}`, `{"n":1}`},
		{"exponent", `{"n":150,"m":0.1}`, `{"n":1.5e2,"m":1E-1}`},
		{"negative zero", `{"n":-0.0}`, `{"n":0}`},
		{"float spelling", `{"c":0.160325}`, `{"c":1.60325e-1}`},
		{"large integer", `{"n":9007199254740993}`, `{"n":9007199254740993.0}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diffs := mustDiff(t, c.want, c.got); diffs != nil {
				t.Fatalf("unexpected differences: %q", diffs)
			}
			EqualJSON(t, []byte(c.want), []byte(c.got))
		})
	}
}

func TestDiffReportsPaths(t *testing.T) {
	cases := []struct{ name, want, got, diff string }{
		{"value", `{"a":{"b":1}}`, `{"a":{"b":2}}`, "$.a.b: got 2, want 1"},
		{"missing key", `{"a":1,"b":2}`, `{"a":1}`, "$.b: missing, want 2"},
		{"extra key", `{"a":1}`, `{"a":1,"cache_control":{"type":"ephemeral"}}`, `$.cache_control: unexpected {"type":"ephemeral"}`},
		{"array length", `[1,2]`, `[1]`, "$: length 1, want 2"},
		{"array element", `{"x":[{"t":"a"}]}`, `{"x":[{"t":"b"}]}`, `$.x[0].t: got "b", want "a"`},
		{"type", `{"a":"1"}`, `{"a":1}`, `$.a: got 1, want "1"`},
		{"number vs string", `{"a":1}`, `{"a":"1"}`, `$.a: got "1", want 1`},
		{"null vs missing", `{"a":null}`, `{}`, "$.a: missing, want null"},
		{"precision beyond float64", `{"n":9007199254740993}`, `{"n":9007199254740992}`, "$.n: got 9007199254740992, want 9007199254740993"},
		{"float", `{"n":0.1}`, `{"n":0.2}`, "$.n: got 0.2, want 0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diffs := mustDiff(t, c.want, c.got)
			if !slices.Equal(diffs, []string{c.diff}) {
				t.Fatalf("got %q, want [%q]", diffs, c.diff)
			}
		})
	}
}

func TestDiffCapsItsReport(t *testing.T) {
	var want, got []int
	for i := range 50 {
		want, got = append(want, i), append(got, i+1)
	}
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	if diffs := mustDiff(t, string(w), string(g)); len(diffs) != maxDiffs {
		t.Fatalf("got %d differences, want %d", len(diffs), maxDiffs)
	}
}

func TestCanonical(t *testing.T) {
	got, err := Canonical([]byte(`{ "b" : [1.0, "<&>", 2.5e0, true], "a" : null, "é" : {"z":1,"y":-0} }`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":null,"b":[1,"<&>",2.5,true],"é":{"y":0,"z":1}}`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestRejectsInvalidJSON(t *testing.T) {
	for _, bad := range []string{``, `{"a":}`, `{"a":1} {"b":2}`, `[1,]`, `"\x"`} {
		if _, err := Canonical([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
		if _, err := Diff([]byte(`{}`), []byte(bad)); err == nil {
			t.Errorf("%q as got: no error", bad)
		}
	}
}

func TestAnthropicGoldensLoad(t *testing.T) {
	m := LoadManifest(t, "anthropic")
	if m.Slice != "anthropic" || len(m.FreezeCommit) != 40 || len(m.Scenarios) == 0 {
		t.Fatalf("manifest: %+v", m)
	}
	listed := map[string]bool{"manifest.json": true, "prompt_stability.json": true, "README.md": true}
	for _, entry := range m.Scenarios {
		listed[entry.File] = true
		if entry.File != entry.Name+".json" || len(entry.SourceTests) == 0 {
			t.Errorf("%s: entry %+v", entry.Name, entry)
		}
		s := LoadScenario(t, "anthropic", entry.Name)
		if s.FreezeCommit != m.FreezeCommit || len(s.Exchanges) != entry.Requests || len(s.Exchanges) == 0 {
			t.Errorf("%s: freeze %s, %d exchanges, manifest says %d", entry.Name, s.FreezeCommit, len(s.Exchanges), entry.Requests)
		}
		for i, x := range s.Exchanges {
			if x.Request.Method != "POST" || x.Request.Path != "/v1/messages" {
				t.Errorf("%s[%d]: %s %s", entry.Name, i, x.Request.Method, x.Request.Path)
			}
			canonical, err := Canonical(x.Request.Body)
			if err != nil {
				t.Fatalf("%s[%d]: %v", entry.Name, i, err)
			}
			EqualJSON(t, x.Request.Body, canonical)
		}
	}
	files, err := os.ReadDir(filepath.Join(Dir(), "anthropic"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !listed[f.Name()] {
			t.Errorf("%s is not in the manifest", f.Name())
		}
	}
	stability := ReadFile(t, "anthropic", "prompt_stability.json")
	if _, err := Canonical(stability); err != nil || !strings.Contains(string(stability), m.FreezeCommit) {
		t.Fatalf("prompt_stability.json: %v", err)
	}
}
