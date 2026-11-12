package module

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/settings"
)

func Compile(decls []Declaration) (*Catalog, error) {
	reg := settings.New()
	byName := make(map[string]Declaration, len(decls))
	for _, d := range decls {
		if strings.TrimSpace(d.Name) == "" {
			return nil, &InvalidDeclaration{Module: d.Name, Field: "name"}
		}
		if _, ok := byName[d.Name]; ok {
			return nil, DuplicateModule(d.Name)
		}
		byName[d.Name] = d
	}
	core := ""
	for _, d := range decls {
		if !d.Core {
			continue
		}
		if core != "" {
			return nil, &InvalidCore{Reason: "exactly one module must be core, found " + core + " and " + d.Name}
		}
		core = d.Name
	}
	switch {
	case core == "":
		return nil, &InvalidCore{Reason: "exactly one module must be core"}
	case core != Core:
		return nil, &InvalidCore{Module: core, Reason: "must be named " + Core}
	}
	for _, d := range decls {
		if d.Core && (len(d.Needs) > 0 || len(d.Requires) > 0) {
			return nil, &InvalidCore{Module: d.Name}
		}
		for _, dep := range d.Needs {
			if _, ok := byName[dep]; !ok {
				return nil, &UnknownDependency{Module: d.Name, Dependency: dep}
			}
		}
	}
	if _, err := byNeeds(decls); err != nil {
		return nil, err
	}
	commands := map[string]bool{}
	for _, d := range decls {
		if !d.Core {
			if _, ok := reg.Lookup(FlagKey(d.Name)); !ok {
				return nil, FlagNotRegistered(FlagKey(d.Name))
			}
		}
		for _, rq := range d.Requires {
			if rq.Key == "" || rq.Host || rq.Connection {
				continue
			}
			if _, ok := reg.Lookup(rq.Key); !ok {
				return nil, settings.UnknownKey(rq.Key)
			}
		}
		groups := map[string]bool{}
		for _, g := range d.Groups {
			if g.Name == "" || g.Title == "" || groups[g.Name] {
				return nil, &InvalidGroup{Module: d.Name, Group: g.Name}
			}
			groups[g.Name] = true
			for _, key := range g.Keys {
				def, ok := reg.Lookup(key)
				if !ok {
					return nil, settings.UnknownKey(key)
				}
				if !def.Editable() {
					return nil, &InvalidGroup{Module: d.Name, Group: g.Name}
				}
			}
		}
		for _, c := range d.Commands {
			if commands[c.Name] {
				return nil, DuplicateCommand(c.Name)
			}
			commands[c.Name] = true
		}
		for _, p := range d.Pages {
			if p.Module == nil && p.Start == nil && len(p.Globals) == 0 && p.Templates == nil && len(p.Nav) == 0 {
				return nil, &InvalidPage{Module: d.Name}
			}
		}
	}
	for _, d := range decls {
		for path, leaf := range d.Leaves {
			if !hasPath(d.Commands, strings.Fields(path)) {
				return nil, &UnknownLeaf{Module: d.Name, Path: path}
			}
			if leaf.Always && len(leaf.Needs) > 0 {
				return nil, &InvalidLeaf{Module: d.Name, Path: path}
			}
			for _, name := range leaf.Needs {
				if _, ok := byName[name]; !ok {
					return nil, &UnknownDependency{Module: d.Name, Dependency: name}
				}
			}
		}
	}
	tools := map[string]bool{}
	var optional []string
	for _, d := range decls {
		if !d.Core {
			optional = append(optional, d.Name)
		}
		for _, def := range d.Tools {
			if tools[def.Tool.Name] {
				return nil, &DeclarationError{Module: d.Name, Field: "tools", Err: DuplicateTool(def.Tool.Name)}
			}
			tools[def.Tool.Name] = true
		}
	}
	return &Catalog{decls: slices.Clone(decls), optional: optional}, nil
}

func hasPath(cmds []*cli.Command, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, c := range cmds {
		if c.Name != path[0] {
			continue
		}
		if len(path) == 1 {
			return true
		}
		return hasPath(c.Commands, path[1:])
	}
	return false
}
