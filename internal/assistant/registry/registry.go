package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant"
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
}

type Source func() (*Registry, error)

func New() *Registry { return &Registry{binders: map[string]Binder{}} }

func (r *Registry) Add(tools ...tool.Tool) error {
	for _, t := range tools {
		if err := validate(t); err != nil {
			return err
		}
	}
	r.tools = append(r.tools, tools...)
	return nil
}

func (r *Registry) Bind(name string, b Binder) { r.binders[name] = b }

func (r *Registry) All() ([]tool.Tool, error) {
	var dup []string
	for _, t := range r.tools {
		if n := countNamed(r.tools, t.Name); n > 1 && !slices.Contains(dup, t.Name) {
			dup = append(dup, t.Name)
		}
	}
	if len(dup) > 0 {
		slices.Sort(dup)
		return nil, DuplicateError(dup)
	}
	out := slices.Clone(r.tools)
	slices.SortFunc(out, func(a, b tool.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (r *Registry) For(kind tool.Kind) ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return !Offered(kind, t) }), nil
}

func (r *Registry) CheckUndo(known func(op string) bool) error {
	all, err := r.All()
	if err != nil {
		return err
	}
	var bad []string
	for _, t := range all {
		for _, op := range t.Undo {
			if !known(op) {
				bad = append(bad, t.Name+": "+op)
			}
		}
	}
	if len(bad) > 0 {
		return InvalidError("write_undo tools name undo ops with no registered undoer: " + strings.Join(bad, ", "))
	}
	return nil
}

func ToolEnabled(ctx context.Context, q db.Querier, name string) (bool, error) {
	key := assistant.ToolEnabledKey(name)
	if _, ok := settings.Default.Lookup(key); !ok {
		return true, nil
	}
	found, err := settings.Default.Resolve(ctx, q, key, 0)
	return found.Value == "true", err
}

func (r *Registry) Run(ctx context.Context, kind tool.Kind, allow []string, q db.Querier, tainted bool) ([]tool.Tool, error) {
	found, err := r.For(kind)
	if err != nil {
		return nil, err
	}
	out := found[:0]
	for _, t := range found {
		if (allow != nil && !slices.Contains(allow, t.Name)) || Dropped(t.Flags, tainted) {
			continue
		}
		if q != nil {
			on, err := ToolEnabled(ctx, q, t.Name)
			if err != nil {
				return nil, err
			}
			if !on {
				continue
			}
		}
		if b, ok := r.binders[t.Name]; ok && q != nil {
			bound, keep, err := b(ctx, q, t)
			if err != nil {
				return nil, err
			}
			if !keep {
				continue
			}
			if bound.Name != t.Name || bound.Effect != t.Effect || bound.Flags != t.Flags || !slices.Equal(bound.Contexts, t.Contexts) {
				return nil, InvalidError(fmt.Sprintf("binder for %s changed its name, effect, flags or run kinds", t.Name))
			}
			t = bound
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *Registry) SubagentPool() ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return !Offered(tool.Sub, t) }), nil
}

func countNamed(tools []tool.Tool, name string) int {
	n := 0
	for _, t := range tools {
		if t.Name == name {
			n++
		}
	}
	return n
}

func validate(t tool.Tool) error {
	switch {
	case t.Name == "":
		return InvalidError("tool has no name")
	case t.Handler == nil:
		return InvalidError(fmt.Sprintf("tool %s has no handler", t.Name))
	case !t.Effect.Valid():
		return InvalidError(fmt.Sprintf("tool %s has unknown effect %q", t.Name, t.Effect))
	case !t.Flags.Stated():
		return InvalidError(fmt.Sprintf("tool %s does not state its untrusted_output, private_data and external_effect flags", t.Name))
	case t.Effect == tool.WriteUndo && len(t.Undo) == 0:
		return InvalidError(fmt.Sprintf("write_undo tool %s names no undo ops", t.Name))
	case t.Effect != tool.WriteUndo && len(t.Undo) > 0:
		return InvalidError(fmt.Sprintf("tool %s names undo ops but its effect is %s", t.Name, t.Effect))
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
