package registry

import (
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

type Decision string

const (
	Deny      Decision = "deny"
	AllowOnly Decision = "allow_only"
)

type Example struct {
	Tool    string
	Effect  tool.Effect
	Allowed bool
}

type Rule struct {
	Kind     tool.Kind
	Decision Decision
	Effects  []tool.Effect
	Tools    []string
	Examples []Example
}

func (r Rule) matches(t tool.Tool) bool {
	for _, e := range r.Effects {
		if t.Effect == e {
			return true
		}
	}
	for _, n := range r.Tools {
		if t.Name == n {
			return true
		}
	}
	return false
}

func (r Rule) denies(kind tool.Kind, t tool.Tool) bool {
	if r.Kind != kind {
		return false
	}
	if r.Decision == AllowOnly {
		return !r.matches(t)
	}
	return r.matches(t)
}

func denied(rules []Rule, kind tool.Kind, t tool.Tool) bool {
	for _, r := range rules {
		if r.denies(kind, t) {
			return true
		}
	}
	return false
}

func mustPolicy(rules ...Rule) []Rule {
	for _, r := range rules {
		if !r.Kind.Valid() || (r.Decision != Deny && r.Decision != AllowOnly) || len(r.Examples) == 0 {
			panic(fmt.Sprintf("registry: policy rule for %q is incomplete", r.Kind))
		}
		for _, e := range r.Examples {
			probe := tool.Tool{Name: e.Tool, Effect: e.Effect}
			if got := !denied(rules, r.Kind, probe); got != e.Allowed {
				panic(fmt.Sprintf("registry: policy example %s (%s) for %s run: allowed is %t, want %t", e.Tool, e.Effect, r.Kind, got, e.Allowed))
			}
		}
	}
	return rules
}

var Policy = mustPolicy(
	Rule{
		Kind: tool.Cron, Decision: Deny, Effects: []tool.Effect{tool.WriteUndo},
		Examples: []Example{
			{Tool: "notion_update_page", Effect: tool.WriteUndo, Allowed: false},
			{Tool: "mail_search", Effect: tool.Read, Allowed: true},
			{Tool: "notify", Effect: tool.Notify, Allowed: true},
			{Tool: "event_propose", Effect: tool.Propose, Allowed: true},
		},
	},
	Rule{
		Kind: tool.Sub, Decision: AllowOnly, Effects: []tool.Effect{tool.Read},
		Examples: []Example{
			{Tool: "mail_read", Effect: tool.Read, Allowed: true},
			{Tool: "notify", Effect: tool.Notify, Allowed: false},
			{Tool: "event_propose", Effect: tool.Propose, Allowed: false},
			{Tool: "schedule_create", Effect: tool.WriteUndo, Allowed: false},
		},
	},
)

func Offered(kind tool.Kind, t tool.Tool) bool {
	return t.Contexts.Has(kind) && !denied(Policy, kind, t)
}
