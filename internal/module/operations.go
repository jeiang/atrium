package module

import (
	"cmp"
	"net/http"
	"reflect"
	"regexp"
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

type InvalidAPIShape struct {
	Module string
	Target string
	Reason string
}

func (e *InvalidAPIShape) Error() string {
	return "module: " + e.Module + " API binding " + e.Target + " " + e.Reason
}

type DuplicateRoute struct {
	Target  string
	Modules []string
}

func (e *DuplicateRoute) Error() string {
	return "module: API route " + e.Target + " bound by " + strings.Join(e.Modules, " and ")
}

func compileOperations(decls []Declaration) ([]Operation, []Binding, error) {
	owner := map[application.Name]string{}
	defs := map[application.Name]application.Definition{}
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
			defs[def.Name] = def
			ops = append(ops, Operation{Module: d.Name, Definition: def})
		}
	}
	var bindings []Binding
	routes := map[string]string{}
	for _, d := range decls {
		for _, b := range d.Bindings {
			if err := checkBinding(d, b, owner); err != nil {
				return nil, nil, err
			}
			if b.Kind == application.API {
				if prev, ok := routes[b.Target]; ok && prev != d.Name {
					return nil, nil, &DuplicateRoute{Target: b.Target, Modules: []string{prev, d.Name}}
				}
				routes[b.Target] = d.Name
			}
			if b.API != nil {
				if reason := checkAPIShape(b, defs[b.Uses[0]]); reason != "" {
					return nil, nil, &InvalidAPIShape{Module: d.Name, Target: b.Target, Reason: reason}
				}
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

var pathParam = regexp.MustCompile(`\{([^}:]+)(?::[^}]*)?\}`)

func checkAPIShape(b application.Binding, def application.Definition) string {
	shape := b.API
	method, pattern, _ := application.Route(b.Target)
	safe := method == http.MethodGet || method == http.MethodHead
	switch {
	case b.Kind != application.API:
		return "is not an API binding"
	case !shape.Area.Valid():
		return "has unknown area " + string(shape.Area)
	case safe != (def.Kind == application.Query):
		return "uses method " + method + " for a " + string(def.Kind)
	case !shape.Body.Valid():
		return "has unknown body kind " + string(shape.Body)
	case !slices.Contains([]int{200, 201, 202, 204, 206}, shape.Success):
		return "has an invalid success status"
	case !wireStruct(shape.Request):
		return "request is not a named struct"
	case !wireStruct(shape.Response):
		return "response is not a named struct"
	}
	for _, t := range []reflect.Type{shape.Request, shape.Response} {
		if reason := wireType(t, map[reflect.Type]bool{}); reason != "" {
			return reason
		}
	}
	want := map[string]int{}
	for _, m := range pathParam.FindAllStringSubmatch(pattern, -1) {
		want[m[1]]++
	}
	for _, f := range reflect.VisibleFields(shape.Request) {
		if f.Anonymous || !f.IsExported() {
			continue
		}
		param := false
		for _, tag := range []string{"path", "query", "header"} {
			if _, ok := f.Tag.Lookup(tag); ok {
				param = true
			}
		}
		if param && f.Tag.Get("json") != "-" {
			return "request field " + f.Name + " is a parameter without json:\"-\""
		}
		name, ok := f.Tag.Lookup("path")
		if !ok {
			continue
		}
		if want[name] != 1 {
			return "request field " + f.Name + " names path parameter " + name + " missing from the pattern"
		}
		want[name] = 0
	}
	for name, n := range want {
		if n != 0 {
			return "path parameter " + name + " has no request field"
		}
	}
	return ""
}

func wireStruct(t reflect.Type) bool {
	return t != nil && t.Kind() == reflect.Struct && (t.Name() != "" || t.NumField() == 0)
}

func wireType(t reflect.Type, seen map[reflect.Type]bool) string {
	switch t.Kind() {
	case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128, reflect.Uintptr:
		return "has a field of type " + t.String()
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return "has a map without string keys " + t.String()
		}
		return wireType(t.Elem(), seen)
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return wireType(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return ""
		}
		seen[t] = true
		for _, f := range reflect.VisibleFields(t) {
			if f.Anonymous || !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			if reason := wireType(f.Type, seen); reason != "" {
				return reason
			}
		}
	}
	return ""
}
