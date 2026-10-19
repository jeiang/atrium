package registry_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const runCodeSentence = " For several read-only lookups across sources, call run_code once instead of many tool turns."

func systemText(t *testing.T, recorded string) string {
	t.Helper()
	if strings.Count(recorded, runCodeSentence) != 1 {
		t.Fatal("the recorded system prompt lost the run_code sentence: regenerate the golden and revisit prompts.System")
	}
	return strings.Replace(recorded, runCodeSentence, "", 1)
}

func shuffled(t *testing.T, tools []tool.Tool, seed uint64) *registry.Registry {
	t.Helper()
	tools = slices.Clone(tools)
	rng := rand.New(rand.NewPCG(seed, seed))
	rng.Shuffle(len(tools), func(i, j int) { tools[i], tools[j] = tools[j], tools[i] })
	reg := registry.New()
	for i := 0; i < len(tools); {
		n := min(1+rng.IntN(5), len(tools)-i)
		if err := reg.Add(tools[i : i+n]...); err != nil {
			t.Fatal(err)
		}
		i += n
	}
	return reg
}

func kindOf(name string, k registrytest.Kind) (tool.Kind, []string, string) {
	kind := tool.Kind(k.Kind)
	ttl := prompts.TTL5m
	if strings.HasSuffix(name, "_1h") {
		ttl = prompts.TTL1h
	}
	if kind == tool.Sub {
		return kind, k.Tools, ttl
	}
	return kind, nil, ttl
}

func blockParts(t *testing.T, s *registrytest.Stability, k registrytest.Kind) (prompts.Parts, []string) {
	t.Helper()
	var p prompts.Parts
	var texts []string
	for _, b := range k.System {
		text := s.SystemTexts[b.Block]
		switch {
		case b.Block == "system":
			text = systemText(t, text)
		case b.Block == "profile":
			p.Profile = text
		case b.Block == "skills_index":
			p.SkillsIndex = text
		case b.Block == "project":
			p.Project = text
		case strings.HasPrefix(b.Block, "role_addendum:"):
			p.Extra = text
		default:
			t.Fatalf("unknown golden system block %q", b.Block)
		}
		texts = append(texts, text)
	}
	return p, texts
}

func TestPromptStabilityGolden(t *testing.T) {
	s := registrytest.Load(t)
	stubs := s.Stubs(t)
	for name, k := range s.Kinds {
		t.Run(name, func(t *testing.T) {
			kind, allow, ttl := kindOf(name, k)
			cc := prompts.PrefixCacheControl(kind, ttl)
			var wantCC prompts.CacheControl
			if err := json.Unmarshal(k.HistoryCacheControl, &wantCC); err != nil || cc != wantCC {
				t.Fatalf("prefix cache control %+v, golden %s", cc, k.HistoryCacheControl)
			}

			parts, texts := blockParts(t, s, k)
			blocks := prompts.SystemBlocks(parts, cc)
			if len(blocks) != len(k.System) {
				t.Fatalf("%d system blocks, golden %d", len(blocks), len(k.System))
			}
			cached := 0
			for i, b := range blocks {
				if b.Text != texts[i] {
					t.Errorf("system block %d (%s) text differs", i, k.System[i].Block)
				}
				if want := string(k.System[i].CacheControl); (want == "null") != (b.CacheControl == nil) {
					t.Errorf("system block %d (%s) cache_control %v, golden %s", i, k.System[i].Block, b.CacheControl, want)
				} else if b.CacheControl != nil {
					got, _ := json.Marshal(b.CacheControl)
					golden.EqualJSON(t, k.System[i].CacheControl, got)
				}
				if b.CacheControl != nil {
					cached = i + 1
				}
			}
			if cached != k.CachedSystemBlocks {
				t.Errorf("cached system blocks %d, golden %d", cached, k.CachedSystemBlocks)
			}

			for seed := uint64(1); seed <= 5; seed++ {
				tools, err := shuffled(t, stubs, seed).Run(context.Background(), kind, allow, nil)
				if err != nil {
					t.Fatal(err)
				}
				params, err := registry.RequestTools(tools, kind, allow, cc.JSON())
				if err != nil {
					t.Fatal(err)
				}
				names := make([]string, len(params))
				for i, raw := range params {
					var p struct {
						Name         string          `json:"name"`
						CacheControl json.RawMessage `json:"cache_control"`
					}
					if err := json.Unmarshal(raw, &p); err != nil {
						t.Fatal(err)
					}
					names[i] = p.Name
					breakpoint := i == len(params)-1
					if breakpoint != (p.CacheControl != nil) {
						t.Errorf("seed %d: tool %s breakpoint = %v", seed, p.Name, p.CacheControl != nil)
					}
					if breakpoint {
						if len(k.ToolCacheControl) != 1 || k.ToolCacheControl[0].Name != p.Name {
							t.Errorf("seed %d: breakpoint on %s, golden %v", seed, p.Name, k.ToolCacheControl)
						} else {
							golden.EqualJSON(t, k.ToolCacheControl[0].CacheControl, p.CacheControl)
						}
					}
					def, ok := s.Defs[p.Name]
					if !ok {
						t.Fatalf("seed %d: tool %s is not in the golden", seed, p.Name)
					}
					stripped := stripCacheControl(t, raw)
					golden.EqualJSON(t, def, stripped)
				}
				if !slices.Equal(names, k.Tools) {
					t.Fatalf("seed %d: tool order\n got %v\nwant %v", seed, names, k.Tools)
				}
			}
		})
	}
}

func stripCacheControl(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "cache_control")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegistrationOrderDoesNotChangeTheRequestPrefix(t *testing.T) {
	stubs := registrytest.Load(t).Stubs(t)
	cc := prompts.Ephemeral.JSON()
	var first string
	for seed := uint64(1); seed <= 20; seed++ {
		reg := shuffled(t, stubs, seed)
		tools, err := reg.Run(context.Background(), tool.Chat, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		params, err := registry.RequestTools(tools, tool.Chat, nil, cc)
		if err != nil {
			t.Fatal(err)
		}
		joined, _ := json.Marshal(params)
		if seed == 1 {
			first = string(joined)
			continue
		}
		if string(joined) != first {
			t.Fatalf("seed %d: the tools array depends on registration order", seed)
		}
	}
}

func scenarioTools(t *testing.T, scenario string, exchange int) (tools, system json.RawMessage) {
	t.Helper()
	sc := golden.LoadScenario(t, "anthropic", scenario)
	var body struct {
		Tools  json.RawMessage `json:"tools"`
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(sc.Exchanges[exchange].Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Tools, body.System
}

func TestRequestBodiesCarryTheRegistryPrefix(t *testing.T) {
	s := registrytest.Load(t)
	stubs := s.Stubs(t)
	reg := registry.New()
	if err := reg.Add(stubs...); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		scenario string
		exchange int
		kind     tool.Kind
		allow    []string
	}{
		{"chat_tool_turn", 0, tool.Chat, nil},
		{"cache_ttl_1h", 0, tool.Chat, nil},
		{"cache_ttl_1h", 2, tool.Cron, nil},
		{"cron_run", 0, tool.Cron, nil},
		{"nightly_run", 0, tool.Nightly, nil},
		{"project_thread", 0, tool.Chat, nil},
		{"subagent_research", 1, tool.Sub, s.Kinds["sub:web"].Tools},
	} {
		t.Run(c.scenario, func(t *testing.T) {
			recordedTools, recordedSystem := scenarioTools(t, c.scenario, c.exchange)
			var system []struct {
				Text         string          `json:"text"`
				CacheControl json.RawMessage `json:"cache_control"`
			}
			if err := json.Unmarshal(recordedSystem, &system); err != nil {
				t.Fatal(err)
			}
			var cc prompts.CacheControl
			if err := json.Unmarshal(system[0].CacheControl, &cc); err != nil {
				t.Fatal(err)
			}
			tools, err := reg.Run(context.Background(), c.kind, c.allow, nil)
			if err != nil {
				t.Fatal(err)
			}
			params, err := registry.RequestTools(tools, c.kind, c.allow, cc.JSON())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(params)
			golden.EqualJSON(t, recordedTools, got)

			var parts prompts.Parts
			for _, b := range system[1:] {
				switch {
				case strings.HasPrefix(b.Text, "Operator profile"):
					parts.Profile = b.Text
				case strings.HasPrefix(b.Text, "Skills ("):
					parts.SkillsIndex = b.Text
				case strings.HasPrefix(b.Text, "This thread belongs"), strings.HasPrefix(b.Text, "The project's wiki hub"):
					parts.Project = b.Text
				default:
					parts.Extra = b.Text
				}
			}
			blocks := prompts.SystemBlocks(parts, cc)
			if len(blocks) != len(system) {
				t.Fatalf("%d system blocks, recorded %d", len(blocks), len(system))
			}
			for i, b := range blocks {
				want := system[i].Text
				if i == 0 {
					want = systemText(t, want)
				}
				if b.Text != want {
					t.Errorf("system block %d text differs", i)
				}
				if (b.CacheControl != nil) != (system[i].CacheControl != nil) {
					t.Errorf("system block %d cache_control %v, recorded %s", i, b.CacheControl, system[i].CacheControl)
				}
			}
		})
	}
}
