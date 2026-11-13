package module

import (
	"cmp"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Operation struct {
	Module string
	application.Definition
}

type Binding struct {
	Module string
	application.Binding
}

type DuplicateOperation struct {
	Name    application.Name
	Modules []string
}

func (e *DuplicateOperation) Error() string {
	return "module: operation " + string(e.Name) + " declared by " + strings.Join(e.Modules, " and ")
}

type InvalidOperation struct {
	Module string
	Name   application.Name
	Field  string
}

func (e *InvalidOperation) Error() string {
	return "module: " + e.Module + " operation " + string(e.Name) + " has no " + e.Field
}

type InvalidBinding struct {
	Module string
	Kind   application.BindingKind
	Target string
}

func (e *InvalidBinding) Error() string {
	return "module: " + e.Module + " has an invalid " + string(e.Kind) + " binding " + e.Target
}

type UnknownOperation struct {
	Module string
	Target string
	Name   application.Name
}

func (e *UnknownOperation) Error() string {
	return "module: " + e.Module + " binds " + e.Target + " to unknown operation " + string(e.Name)
}

type ForeignOperation struct {
	Module string
	Owner  string
	Name   application.Name
}

func (e *ForeignOperation) Error() string {
	return "module: " + e.Module + " binds operation " + string(e.Name) + " owned by " + e.Owner
}

type UnknownBindingTarget struct {
	Module string
	Kind   application.BindingKind
	Target string
}

func (e *UnknownBindingTarget) Error() string {
	return "module: " + e.Module + " binds missing " + string(e.Kind) + " " + e.Target
}

func compileOperations(decls []Declaration) ([]Operation, []Binding, error) {
	owner := map[application.Name]string{}
	var ops []Operation
	for _, d := range decls {
		for _, def := range d.Operations {
			if prev, ok := owner[def.Name]; ok {
				return nil, nil, &DuplicateOperation{Name: def.Name, Modules: []string{prev, d.Name}}
			}
			if field := missingField(def); field != "" {
				return nil, nil, &InvalidOperation{Module: d.Name, Name: def.Name, Field: field}
			}
			owner[def.Name] = d.Name
			ops = append(ops, Operation{Module: d.Name, Definition: def})
		}
	}
	var bindings []Binding
	for _, d := range decls {
		for _, b := range d.Bindings {
			if err := checkBinding(d, b, owner); err != nil {
				return nil, nil, err
			}
			bindings = append(bindings, Binding{Module: d.Name, Binding: b})
		}
	}
	slices.SortFunc(ops, func(a, b Operation) int {
		return cmp.Or(cmp.Compare(a.Module, b.Module), cmp.Compare(a.Name, b.Name))
	})
	return ops, bindings, nil
}

func missingField(def application.Definition) string {
	switch {
	case strings.TrimSpace(string(def.Name)) == "":
		return "name"
	case def.Kind != application.Command && def.Kind != application.Query:
		return "kind"
	case len(def.Authority) == 0:
		return "authority"
	case def.Kind == application.Command && !def.AuditSet:
		return "audit policy"
	case def.Input == nil:
		return "input type"
	case def.Output == nil:
		return "output type"
	}
	return ""
}

func checkBinding(d Declaration, b application.Binding, owner map[application.Name]string) error {
	invalid := &InvalidBinding{Module: d.Name, Kind: b.Kind, Target: b.Target}
	if len(b.Uses) == 0 || strings.TrimSpace(b.Target) == "" || (b.Composite && !d.Core) {
		return invalid
	}
	switch b.Kind {
	case application.HTML, application.API:
		if _, _, ok := application.Route(b.Target); !ok {
			return invalid
		}
	case application.CLI, application.Tool, application.Job, application.Service:
		if !declares(d, b.Kind, b.Target) {
			return &UnknownBindingTarget{Module: d.Name, Kind: b.Kind, Target: b.Target}
		}
	default:
		return invalid
	}
	for _, name := range b.Uses {
		mod, ok := owner[name]
		if !ok {
			return &UnknownOperation{Module: d.Name, Target: b.Target, Name: name}
		}
		if mod != d.Name && !b.Composite {
			return &ForeignOperation{Module: d.Name, Owner: mod, Name: name}
		}
	}
	return nil
}

func declares(d Declaration, kind application.BindingKind, target string) bool {
	switch kind {
	case application.CLI:
		return hasPath(d.Commands, strings.Fields(target))
	case application.Tool:
		return slices.ContainsFunc(d.Tools, func(t ToolDefinition) bool { return t.Tool.Name == target })
	case application.Job:
		return slices.ContainsFunc(d.Jobs, func(j scheduler.Definition) bool { return j.Key == target })
	case application.Service:
		return slices.ContainsFunc(d.Services, func(s runtime.ServiceDefinition) bool { return s.Name == target })
	}
	return false
}
