package registrytest

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

//go:embed matrix.json
var matrixJSON []byte

type Entry struct {
	Name     string      `json:"name"`
	Effect   tool.Effect `json:"effect"`
	Contexts tool.Kinds  `json:"contexts"`
}

func Matrix(t testing.TB) []Entry {
	t.Helper()
	var rows []Entry
	if err := json.Unmarshal(matrixJSON, &rows); err != nil {
		t.Fatalf("registrytest: matrix.json: %v", err)
	}
	return rows
}

type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Kind struct {
	Kind                string          `json:"kind"`
	CachedSystemBlocks  int             `json:"cached_system_blocks"`
	HistoryCacheControl json.RawMessage `json:"history_cache_control"`
	System              []struct {
		Block        string          `json:"block"`
		CacheControl json.RawMessage `json:"cache_control"`
	} `json:"system"`
	Tools            []string `json:"tools"`
	ToolCacheControl []struct {
		Name         string          `json:"name"`
		CacheControl json.RawMessage `json:"cache_control"`
	} `json:"tool_cache_control"`
}

type Stability struct {
	Kinds       map[string]Kind            `json:"kinds"`
	SystemTexts map[string]string          `json:"system_texts"`
	Defs        map[string]json.RawMessage `json:"tool_definitions"`
}

func Load(t testing.TB) *Stability {
	t.Helper()
	var s Stability
	if err := json.Unmarshal(golden.ReadFile(t, "anthropic", "prompt_stability.json"), &s); err != nil {
		t.Fatalf("registrytest: prompt_stability.json: %v", err)
	}
	return &s
}

func (s *Stability) Definitions(t testing.TB) []Definition {
	t.Helper()
	var out []Definition
	for name, raw := range s.Defs {
		if name == registry.WebSearchName {
			continue
		}
		var d Definition
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("registrytest: tool %s: %v", name, err)
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *Stability) Stubs(t testing.TB) []tool.Tool {
	t.Helper()
	matrix := map[string]Entry{}
	for _, e := range Matrix(t) {
		matrix[e.Name] = e
	}
	var out []tool.Tool
	for _, d := range s.Definitions(t) {
		e, ok := matrix[d.Name]
		if !ok {
			t.Fatalf("registrytest: golden tool %s is not in the matrix", d.Name)
		}
		out = append(out, tool.Tool{
			Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, Effect: e.Effect, Contexts: e.Contexts,
			Handler: stub,
		})
	}
	return out
}

func stub(context.Context, *tool.Context, py.Dict) (any, error) {
	return nil, &tool.Raised{Class: "NotImplementedError"}
}

func Verify(t testing.TB, got tool.Tool) {
	t.Helper()
	s := Load(t)
	raw, ok := s.Defs[got.Name]
	if !ok {
		t.Fatalf("tool %s is not in the Python registry", got.Name)
	}
	var want Definition
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if got.Description != want.Description {
		t.Errorf("tool %s: description differs from Python:\n got %q\nwant %q", got.Name, got.Description, want.Description)
	}
	wantSchema, _ := json.Marshal(want.InputSchema)
	golden.EqualJSON(t, wantSchema, got.InputSchema)
	for _, e := range Matrix(t) {
		if e.Name != got.Name {
			continue
		}
		if got.Effect != e.Effect {
			t.Errorf("tool %s: effect %q, Python %q", got.Name, got.Effect, e.Effect)
		}
		a, b := slices.Clone(got.Contexts), slices.Clone(e.Contexts)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Errorf("tool %s: run kinds %v, Python %v", got.Name, got.Contexts, e.Contexts)
		}
	}
}
