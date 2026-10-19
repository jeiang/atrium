package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func handler(context.Context, *tool.Context, py.Dict) (any, error) { return "ok", nil }

func simple(name string, effect tool.Effect, kinds ...tool.Kind) tool.Tool {
	return tool.Tool{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`), Handler: handler, Effect: effect, Contexts: kinds}
}

func names(tools []tool.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func TestToolMatrix(t *testing.T) {
	s := registrytest.Load(t)
	matrix := registrytest.Matrix(t)
	if len(matrix) != 59 {
		t.Fatalf("matrix has %d tools, want the 59 of the frozen registry without run_code", len(matrix))
	}
	defs := s.Definitions(t)
	if len(defs) != len(matrix) {
		t.Fatalf("golden defines %d function tools, matrix %d", len(defs), len(matrix))
	}
	reg := registry.New()
	if err := reg.Add(s.Stubs(t)...); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []tool.Kind{tool.Chat, tool.Cron, tool.Nightly} {
		var want []string
		for _, e := range matrix {
			if e.Contexts.Has(kind) {
				want = append(want, e.Name)
			}
		}
		got, err := reg.For(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(names(got), want) {
			t.Errorf("%s tools\n got %v\nwant %v", kind, names(got), want)
		}
		var recorded []string
		for _, n := range s.Kinds[string(kind)].Tools {
			if n != registry.WebSearchName {
				recorded = append(recorded, n)
			}
		}
		if !slices.Equal(recorded, want) {
			t.Errorf("%s: the recorded request has\n %v\nthe matrix has\n %v", kind, recorded, want)
		}
	}
	for name, k := range s.Kinds {
		if k.Kind != string(tool.Sub) {
			continue
		}
		for _, n := range k.Tools {
			if n == registry.WebSearchName {
				continue
			}
			found := false
			for _, e := range matrix {
				found = found || e.Name == n && e.Effect == tool.Read && e.Contexts.Has(tool.Sub)
			}
			if !found {
				t.Errorf("%s offers %s, which is not a read tool of the sub kind", name, n)
			}
		}
	}
}

func TestToolPolicyInvariants(t *testing.T) {
	for _, e := range registrytest.Matrix(t) {
		if e.Contexts.Has(tool.Sub) && e.Effect != tool.Read {
			t.Errorf("%s: subagents are read-only, the tool is %s", e.Name, e.Effect)
		}
		if e.Contexts.Has(tool.Cron) && e.Effect == tool.Write {
			t.Errorf("%s: scheduled runs only read, propose and notify, the tool writes", e.Name)
		}
		if e.Contexts.Has(tool.Nightly) && e.Effect == tool.Propose {
			t.Errorf("%s: the nightly run does not propose", e.Name)
		}
	}
	nightlyWrites := map[string]bool{
		"notion_create_page": true, "notion_update_page": true, "notion_move_page": true,
		"notion_archive_page": true, "notion_add_alias": true, "profile_set": true,
	}
	for _, e := range registrytest.Matrix(t) {
		if e.Contexts.Has(tool.Nightly) && e.Effect == tool.Write && !nightlyWrites[e.Name] {
			t.Errorf("%s: the nightly run writes only Notion and the profile", e.Name)
		}
	}
	for _, name := range []string{"web_fetch", "web_search"} {
		for _, e := range registrytest.Matrix(t) {
			if e.Name == name && e.Contexts.Has(tool.Nightly) {
				t.Errorf("%s must not reach the nightly run", name)
			}
		}
	}
}

func TestSubagentPool(t *testing.T) {
	reg := registry.New()
	if err := reg.Add(registrytest.Load(t).Stubs(t)...); err != nil {
		t.Fatal(err)
	}
	pool, err := reg.SubagentPool()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range registrytest.Matrix(t) {
		if e.Effect == tool.Read && e.Contexts.Has(tool.Sub) {
			want = append(want, e.Name)
		}
	}
	if !slices.Equal(names(pool), want) {
		t.Errorf("pool\n got %v\nwant %v", names(pool), want)
	}
	for _, banned := range []string{"research", "plan", "notify", "load_skill", "context_status"} {
		if slices.Contains(names(pool), banned) {
			t.Errorf("%s is in the subagent pool", banned)
		}
	}
}

func TestDuplicateNamesAreRefused(t *testing.T) {
	reg := registry.New()
	if err := reg.Add(simple("b", tool.Read, tool.Chat), simple("a", tool.Read, tool.Chat), simple("b", tool.Read, tool.Cron), simple("a", tool.Read, tool.Sub)); err != nil {
		t.Fatal(err)
	}
	_, err := reg.All()
	var dup registry.DuplicateError
	if !errors.As(err, &dup) || err.Error() != "duplicate tool names: a, b" {
		t.Errorf("All() = %v, want the sorted duplicate names", err)
	}
	if _, err := reg.Run(context.Background(), tool.Chat, nil, nil); err == nil {
		t.Error("Run lists nothing while names collide")
	}
}

func TestAddValidates(t *testing.T) {
	good := simple("t", tool.Read, tool.Chat)
	for name, mutate := range map[string]func(*tool.Tool){
		"no name":        func(t *tool.Tool) { t.Name = "" },
		"no handler":     func(t *tool.Tool) { t.Handler = nil },
		"bad effect":     func(t *tool.Tool) { t.Effect = "" },
		"no kinds":       func(t *tool.Tool) { t.Contexts = nil },
		"code kind":      func(t *tool.Tool) { t.Contexts = tool.Kinds{"code"} },
		"schema array":   func(t *tool.Tool) { t.InputSchema = json.RawMessage(`[]`) },
		"schema invalid": func(t *tool.Tool) { t.InputSchema = json.RawMessage(`{`) },
		"schema null":    func(t *tool.Tool) { t.InputSchema = json.RawMessage(`null`) },
		"schema empty":   func(t *tool.Tool) { t.InputSchema = nil },
	} {
		bad := good
		mutate(&bad)
		var invalid registry.InvalidError
		if err := registry.New().Add(bad); !errors.As(err, &invalid) {
			t.Errorf("%s: Add = %v, want InvalidError", name, err)
		}
	}
	if err := registry.New().Add(good); err != nil {
		t.Errorf("a valid tool is refused: %v", err)
	}
}

func TestRunNarrowsByAllowlist(t *testing.T) {
	reg := registry.New()
	if err := reg.Add(simple("a", tool.Read, tool.Chat, tool.Sub), simple("b", tool.Read, tool.Chat, tool.Sub), simple("c", tool.Write, tool.Chat)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		kind  tool.Kind
		allow []string
		want  []string
	}{
		{"no allowlist", tool.Chat, nil, []string{"a", "b", "c"}},
		{"allowlist", tool.Chat, []string{"b", "zzz"}, []string{"b"}},
		{"empty allowlist allows nothing", tool.Chat, []string{}, []string{}},
		{"kind comes first", tool.Sub, []string{"a", "c"}, []string{"a"}},
		{"no tools of the kind", tool.Nightly, nil, []string{}},
	} {
		got, err := reg.Run(ctx, c.kind, c.allow, nil)
		if err != nil || !slices.Equal(names(got), c.want) {
			t.Errorf("%s: %v, %v; want %v", c.name, names(got), err, c.want)
		}
	}
}

func TestBinderRebindsOrDropsATool(t *testing.T) {
	reg := registry.New()
	if err := reg.Add(simple("research", tool.Read, tool.Chat), simple("other", tool.Read, tool.Chat)); err != nil {
		t.Fatal(err)
	}
	enabled := true
	reg.Bind("research", func(_ context.Context, q db.Querier, t tool.Tool) (tool.Tool, bool, error) {
		t.Description = "bound"
		return t, enabled, nil
	})
	ctx := context.Background()
	var q db.Querier = struct{ db.Querier }{}
	got, _ := reg.Run(ctx, tool.Chat, nil, nil)
	if got[1].Description != "research" {
		t.Error("without a database nothing is bound")
	}
	got, _ = reg.Run(ctx, tool.Chat, nil, q)
	if len(got) != 2 || got[1].Name != "research" || got[1].Description != "bound" || got[0].Description != "other" {
		t.Errorf("bound run: %v", got)
	}
	enabled = false
	got, _ = reg.Run(ctx, tool.Chat, nil, q)
	if !slices.Equal(names(got), []string{"other"}) {
		t.Errorf("a tool its binder drops is not offered: %v", names(got))
	}
	fail := errors.New("db down")
	reg.Bind("research", func(context.Context, db.Querier, tool.Tool) (tool.Tool, bool, error) { return tool.Tool{}, false, fail })
	if _, err := reg.Run(ctx, tool.Chat, nil, q); !errors.Is(err, fail) {
		t.Errorf("binder error = %v", err)
	}
}

func TestServerTools(t *testing.T) {
	has := func(kind tool.Kind, allow []string) bool { return len(registry.ServerTools(kind, allow)) == 1 }
	if !has(tool.Chat, nil) || !has(tool.Cron, nil) || has(tool.Nightly, nil) || has(tool.Sub, nil) || has(tool.Sub, []string{"recall"}) || !has(tool.Sub, []string{"recall", "web_search"}) {
		t.Error("web_search is offered to chat and cron, and to a subagent only through its allowlist")
	}
	if has(tool.Nightly, []string{"web_search"}) {
		t.Error("the nightly run never searches the web")
	}
	got := registry.ServerTools(tool.Chat, nil)[0]
	want := `{"type":"web_search_20260318","name":"web_search","max_uses":5,"allowed_callers":["direct"]}`
	if string(got) != want {
		t.Errorf("web_search = %s", got)
	}
	got[0] = 'X'
	if string(registry.ServerTools(tool.Chat, nil)[0]) != want {
		t.Error("ServerTools hands out the shared declaration")
	}
}

func TestRequestToolsMarksTheLastTool(t *testing.T) {
	cc := json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
	one := []tool.Tool{simple("a", tool.Read, tool.Chat)}
	params, err := registry.RequestTools(one, tool.Nightly, nil, cc)
	if err != nil || len(params) != 1 {
		t.Fatalf("%v, %v", params, err)
	}
	if want := `{"name":"a","description":"a","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}`; string(params[0]) != want {
		t.Errorf("got %s", params[0])
	}
	params, _ = registry.RequestTools(one, tool.Chat, nil, cc)
	if len(params) != 2 || string(params[0]) != `{"name":"a","description":"a","input_schema":{"type":"object"}}` {
		t.Errorf("the breakpoint moves to the server tool: %s", params)
	}
	if params, _ := registry.RequestTools(nil, tool.Nightly, nil, cc); params != nil {
		t.Errorf("no tools, no tools array: %s", params)
	}
	html := tool.Tool{Name: "h", Description: "a < b & c > d", InputSchema: json.RawMessage(`{"type":"object"}`), Handler: handler, Effect: tool.Read, Contexts: tool.Kinds{tool.Chat}}
	params, _ = registry.Params([]tool.Tool{html})
	if string(params[0]) != `{"name":"h","description":"a < b & c > d","input_schema":{"type":"object"}}` {
		t.Errorf("HTML is escaped: %s", params[0])
	}
}

func TestBinderCannotChangePolicy(t *testing.T) {
	for name, mutate := range map[string]func(*tool.Tool){
		"name":     func(t *tool.Tool) { t.Name = "x" },
		"effect":   func(t *tool.Tool) { t.Effect = tool.Write },
		"contexts": func(t *tool.Tool) { t.Contexts = tool.Kinds{tool.Chat, tool.Nightly} },
	} {
		reg := registry.New()
		if err := reg.Add(simple("research", tool.Read, tool.Chat)); err != nil {
			t.Fatal(err)
		}
		reg.Bind("research", func(_ context.Context, _ db.Querier, t tool.Tool) (tool.Tool, bool, error) {
			mutate(&t)
			return t, true, nil
		})
		var invalid registry.InvalidError
		_, err := reg.Run(context.Background(), tool.Chat, nil, struct{ db.Querier }{})
		if !errors.As(err, &invalid) {
			t.Errorf("%s: Run = %v, want InvalidError", name, err)
		}
	}
}
