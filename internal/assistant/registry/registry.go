// Package registry is registry.py and tools/__init__.py: the set of tools one instance offers, narrowed per run
// kind and per subagent allowlist. Tool policy is enforced here by each tool's Contexts, never by the prompt.
package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
)

// DuplicateError is the ValueError all_tools raises when two tools share a name.
type DuplicateError []string

func (e DuplicateError) Error() string { return "duplicate tool names: " + strings.Join(e, ", ") }

// InvalidError reports a tool that cannot be registered.
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

// Binder rebinds one tool for a run from the database (research.with_agents): ok false drops the tool.
type Binder func(ctx context.Context, q db.Querier, t tool.Tool) (bound tool.Tool, ok bool, err error)

// Registry holds registered tools. Registration order never matters: every listing is sorted by name.
type Registry struct {
	tools   []tool.Tool
	binders map[string]Binder
}

// New is an empty registry.
func New() *Registry { return &Registry{binders: map[string]Binder{}} }

// Add registers tools. A tool needs a name, a JSON object schema, a handler, a known effect and known kinds.
func (r *Registry) Add(tools ...tool.Tool) error {
	for _, t := range tools {
		if err := validate(t); err != nil {
			return err
		}
	}
	r.tools = append(r.tools, tools...)
	return nil
}

// Bind sets the Binder Run applies to the tool called name when it is given a database.
func (r *Registry) Bind(name string, b Binder) { r.binders[name] = b }

// All is all_tools: every tool sorted by name; two tools with one name are a DuplicateError.
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

// For is tools_for: the tools whose contexts include kind, sorted by name.
func (r *Registry) For(kind tool.Kind) ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return !t.Contexts.Has(kind) }), nil
}

// Run is run_tools: For(kind) narrowed to allow (a subagent's allowlist; nil is no allowlist, an empty one
// allows nothing), so a child never sees more than its agent definition grants. With q, a tool that has a
// Binder is rebound to the database and dropped when its Binder says so.
func (r *Registry) Run(ctx context.Context, kind tool.Kind, allow []string, q db.Querier) ([]tool.Tool, error) {
	found, err := r.For(kind)
	if err != nil {
		return nil, err
	}
	out := found[:0]
	for _, t := range found {
		if allow != nil && !slices.Contains(allow, t.Name) {
			continue
		}
		if b, ok := r.binders[t.Name]; ok && q != nil {
			bound, keep, err := b(ctx, q, t)
			if err != nil {
				return nil, err
			}
			if !keep {
				continue
			}
			t = bound
		}
		out = append(out, t)
	}
	return out, nil
}

// SubagentPool is subagent_pool: the read-effect tools offered to subagents, sorted by name; the tools an
// operator may put on a subagent allowlist.
func (r *Registry) SubagentPool() ([]tool.Tool, error) {
	all, err := r.All()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(t tool.Tool) bool { return t.Effect != tool.Read || !t.Contexts.Has(tool.Sub) }), nil
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
