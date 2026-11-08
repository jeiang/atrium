package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type DuplicateError []string

func (e DuplicateError) Error() string { return "duplicate tool names: " + strings.Join(e, ", ") }

type InvalidError string

func (e InvalidError) Error() string { return string(e) }

type Binder func(ctx context.Context, q db.Querier, t tool.Tool) (bound tool.Tool, ok bool, err error)

type Registry struct {
	tools   []tool.Tool
	binders map[string]Binder
	mu      sync.Mutex
	tainted map[int64]bool
}

type Source func() (*Registry, error)

func New() *Registry {
	return &Registry{tools: []tool.Tool{webSearchTool()}, binders: map[string]Binder{}, tainted: map[int64]bool{}}
}

func (r *Registry) Add(tools ...tool.Tool) error {
	for _, t := range tools {
		if err := validate(t); err != nil {
			return err
		}
		if slices.ContainsFunc(r.tools, func(e tool.Tool) bool { return e.Name == t.Name }) {
			return DuplicateError([]string{t.Name})
		}
	}
	for _, t := range tools {
		key := settingKey(t.Name)
		if _, ok := settings.Default.Lookup(key); ok {
			continue
		}
		if err := settings.Default.Register(settings.Value(settings.Advanced, settings.Def{
			Key: key, Module: "tools", Type: settings.Bool, Default: "true",
		})); err != nil {
			return err
		}
	}
	r.tools = append(r.tools, tools...)
	return nil
}

func settingKey(name string) string { return "tool_" + name + "_enabled" }

func (r *Registry) Taint(threadID int64, t tool.Tool) {
	if !t.Flags.PrivateData && !t.Flags.Web {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tainted[threadID] = true
}

func (r *Registry) Tainted(threadID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tainted[threadID]
}

type Decision int

const (
	Allowed Decision = iota
	Ask
	Denied
)

type Rule struct {
	Kind     tool.Kind
	Effect   tool.Effect
	Decision Decision
}

var Policy = []Rule{
	{Kind: tool.Cron, Effect: tool.WriteUndo, Decision: Denied},
	{Kind: tool.Nightly, Effect: tool.WriteUndo, Decision: Denied},
	{Kind: tool.Sub, Effect: tool.WriteUndo, Decision: Denied},
}

func Decide(rules []Rule, kind tool.Kind, effect tool.Effect) Decision {
	decision := Allowed
	for _, rule := range rules {
		if rule.Kind == kind && rule.Effect == effect && rule.Decision > decision {
			decision = rule.Decision
		}
	}
	return decision
}

func ValidateAnnotations(tools []tool.Tool) error {
	var missing []string
	for _, t := range tools {
		if !t.Annotated {
			missing = append(missing, t.Name)
		}
	}
	if len(missing) > 0 {
		return InvalidError("tools missing annotations: " + strings.Join(missing, ", "))
	}
	return nil
}

func (r *Registry) Bind(name string, b Binder) { r.binders[name] = b }

func (r *Registry) All() ([]tool.Tool, error) {
	out := slices.Clone(r.tools)
	slices.SortFunc(out, func(a, b tool.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (r *Registry) For(kind tool.Kind) ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return !t.Contexts.Has(kind) }), nil
}

type Allow struct {
	all   bool
	names map[string]struct{}
}

func All() Allow { return Allow{all: true} }

func Only(names []string) Allow {
	a := Allow{names: make(map[string]struct{}, len(names))}
	for _, n := range names {
		a.names[n] = struct{}{}
	}
	return a
}

func (a Allow) Has(name string) bool {
	if a.all {
		return true
	}
	_, ok := a.names[name]
	return ok
}

type Set struct {
	Dispatch []tool.Tool
	Server   []tool.Tool
}

func (s Set) Has(name string) bool {
	return slices.ContainsFunc(s.Dispatch, func(t tool.Tool) bool { return t.Name == name }) ||
		slices.ContainsFunc(s.Server, func(t tool.Tool) bool { return t.Name == name })
}

func (r *Registry) Admit(ctx context.Context, threadID int64, kind tool.Kind, allow Allow, q db.Querier) (Set, error) {
	found, err := r.For(kind)
	if err != nil {
		return Set{}, err
	}
	tainted := r.Tainted(threadID)
	var set Set
	for _, t := range found {
		if !allow.Has(t.Name) {
			continue
		}
		if Decide(Policy, kind, t.Effect) == Denied {
			continue
		}
		if tainted && (t.Flags.ExternalEffect || t.Effect == tool.Notify) {
			continue
		}
		enabled, err := r.enabled(ctx, q, t.Name)
		if err != nil {
			return Set{}, err
		}
		if !enabled {
			continue
		}
		if t.Server {
			set.Server = append(set.Server, t)
			continue
		}
		if b, ok := r.binders[t.Name]; ok && q != nil {
			bound, keep, err := b(ctx, q, t)
			if err != nil {
				return Set{}, err
			}
			if !keep {
				continue
			}
			if bound.Name != t.Name || bound.Effect != t.Effect || !slices.Equal(bound.Contexts, t.Contexts) {
				return Set{}, InvalidError(fmt.Sprintf("binder for %s changed its name, effect or run kinds", t.Name))
			}
			t = bound
		}
		set.Dispatch = append(set.Dispatch, t)
	}
	return set, nil
}

func (r *Registry) SubagentPool() ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return t.Effect != tool.Read || !t.Contexts.Has(tool.Sub) }), nil
}

func (r *Registry) enabled(ctx context.Context, q db.Querier, name string) (bool, error) {
	key := settingKey(name)
	if _, ok := settings.Default.Lookup(key); !ok || q == nil {
		return true, nil
	}
	found, err := settings.Default.Resolve(ctx, q, key)
	if err != nil {
		return false, err
	}
	return found.Value != "false", nil
}

func validate(t tool.Tool) error {
	switch {
	case t.Name == "":
		return InvalidError("tool has no name")
	case t.Effect == tool.WriteUndo && t.Undo == "":
		return InvalidError(fmt.Sprintf("tool %s is write_undo without an undo op", t.Name))
	case !t.Annotated:
		return InvalidError(fmt.Sprintf("tool %s is not annotated", t.Name))
	case t.Handler == nil && !t.Server:
		return InvalidError(fmt.Sprintf("tool %s has no handler", t.Name))
	case t.Handler != nil && t.Server:
		return InvalidError(fmt.Sprintf("server tool %s has a handler", t.Name))
	case !t.Effect.Valid():
		return InvalidError(fmt.Sprintf("tool %s has unknown effect %q", t.Name, t.Effect))
	case len(t.Contexts) == 0:
		return InvalidError(fmt.Sprintf("tool %s has no run kinds", t.Name))
	}
	for _, k := range t.Contexts {
		if !k.Valid() {
			return InvalidError(fmt.Sprintf("tool %s has unknown run kind %q", t.Name, k))
		}
	}
	if !isObject(t.InputSchema) {
		return InvalidError(fmt.Sprintf("tool %s input_schema is not a JSON object", t.Name))
	}
	return nil
}
