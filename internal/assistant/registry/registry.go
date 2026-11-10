package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

type DuplicateError []string

func (e DuplicateError) Error() string { return "duplicate tool names: " + strings.Join(e, ", ") }

type InvalidError string

func (e InvalidError) Error() string { return string(e) }

type Binder func(ctx context.Context, q db.Querier, t tool.Tool) (bound tool.Tool, ok bool, err error)

type Registry struct {
	tools    []tool.Tool
	binders  map[string]Binder
	settings *settings.Registry
}

type Source func() (*Registry, error)

func New(reg *settings.Registry) *Registry {
	return &Registry{tools: []tool.Tool{webSearchTool()}, binders: map[string]Binder{}, settings: reg}
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
		if _, ok := r.settings.Lookup(key); ok {
			continue
		}
		if err := r.settings.Register(settings.Value(settings.Advanced, settings.Def{
			Key: key, Module: "tools", Type: settings.Bool, Default: "true",
		})); err != nil {
			return err
		}
	}
	r.tools = append(r.tools, tools...)
	return nil
}

func settingKey(name string) string { return "tool_" + name + "_enabled" }

type Decision int

const (
	Allowed Decision = iota
	Ask
	Denied
)

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

func (r *Registry) Admit(ctx context.Context, kind tool.Kind, role *string, webSearch bool, marks threads.Marks, allow Allow, q db.Querier) (Set, error) {
	found, err := r.For(kind)
	if err != nil {
		return Set{}, err
	}
	var candidates []tool.Tool
	privateTools := false
	for _, t := range found {
		if !allow.Has(t.Name) {
			continue
		}
		if t.Name == "search_web" && !webSearch {
			continue
		}
		if t.Server && t.Name == WebSearchName && (role == nil || *role != "search_web") {
			continue
		}
		enabled, err := r.enabled(ctx, q, t.Name)
		if err != nil {
			return Set{}, err
		}
		if !enabled {
			continue
		}
		privateTools = privateTools || t.Marks.Kinds() != 0
		candidates = append(candidates, t)
	}
	situation := Classify(marks.Set, PromptCarriesKnowledge(kind), privateTools)
	table, err := r.loadTable(ctx, q)
	if err != nil {
		return Set{}, err
	}
	var set Set
	for _, t := range candidates {
		if marks.Set != 0 && t.PromptStore {
			continue
		}
		decision := table.Decide(t.Channel, situation, kind)
		if decision == Ask && !hasCard(t) && kind != tool.Chat {
			decision = Denied
		}
		switch {
		case decision == Denied:
			continue
		case t.Server:
			if decision != Allowed {
				continue
			}
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
			if bound.Name != t.Name || bound.Effect != t.Effect || bound.Channel != t.Channel || !slices.Equal(bound.Contexts, t.Contexts) {
				return Set{}, InvalidError(fmt.Sprintf("binder for %s changed its name, effect, channel or run kinds", t.Name))
			}
			t = bound
		}
		if t.ChannelOf != nil || (decision == Ask && !hasCard(t)) {
			t = Gate(t, table, situation, kind)
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
	return slices.DeleteFunc(all, func(t tool.Tool) bool {
		return t.Name == "report" || t.Name == WebSearchName || t.Effect != tool.Read || !t.Contexts.Has(tool.Sub)
	}), nil
}

func (r *Registry) enabled(ctx context.Context, q db.Querier, name string) (bool, error) {
	key := settingKey(name)
	if _, ok := r.settings.Lookup(key); !ok || q == nil {
		return true, nil
	}
	found, err := r.settings.Resolve(ctx, q, key)
	if err != nil {
		return false, err
	}
	return found.Value != "false", nil
}

func sharedWorkerTool(name string) bool {
	return name == "kb_search" || name == "kb_get" || name == "atrium_self_diagnose" || strings.HasPrefix(name, "grafana_")
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
	case t.Marks&^tool.AllMarks != 0:
		return InvalidError(fmt.Sprintf("tool %s has unknown marks %d", t.Name, t.Marks))
	case len(t.Contexts) == 0:
		return InvalidError(fmt.Sprintf("tool %s has no run kinds", t.Name))
	case !t.Channel.Valid():
		return InvalidError(fmt.Sprintf("tool %s has unknown channel %d", t.Name, t.Channel))
	case t.Effect == tool.WriteUndo && t.Channel != tool.ChannelOwn:
		return InvalidError(fmt.Sprintf("tool %s writes directly but is not on the own-accounts channel", t.Name))
	case (t.Effect == tool.Notify) != (t.Channel == tool.ChannelNotify):
		return InvalidError(fmt.Sprintf("tool %s: the notify effect and the notify channel go together", t.Name))
	case t.Server && t.Channel != tool.ChannelInternet:
		return InvalidError(fmt.Sprintf("tool %s is a server tool outside the internet channel", t.Name))
	case (t.Channel == tool.ChannelPeople || t.Channel == tool.ChannelInternet) && t.Effect != tool.Read && !hasCard(t):
		return InvalidError(fmt.Sprintf("tool %s reaches people or the internet without a card", t.Name))
	case t.PromptStore && t.Effect != tool.WriteUndo:
		return InvalidError(fmt.Sprintf("tool %s is a prompt store but not a direct write", t.Name))
	case t.Effect == tool.WriteUndo && t.Contexts.Has(tool.Sub):
		return InvalidError(fmt.Sprintf("tool %s writes directly in sub-agent runs", t.Name))
	case t.Approval != nil && (t.Effect != tool.Propose || t.Approval.Apply == nil || t.Approval.Label == "" || t.Approval.Module == ""):
		return InvalidError(fmt.Sprintf("tool %s has an incomplete approval", t.Name))
	case t.Contexts.Has(tool.Worker) && len(t.Contexts) > 1 && !sharedWorkerTool(t.Name):
		return InvalidError(fmt.Sprintf("tool %s is available to worker runs and other run kinds", t.Name))
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
