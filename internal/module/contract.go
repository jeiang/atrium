package module

import (
	"path"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
)

type InvalidDependency struct{ Module, Dependency string }

func (e *InvalidDependency) Error() string {
	return "module: " + e.Module + " is a core unit and depends on the app " + e.Dependency + "; a core unit never depends on an app"
}

type TableOwners struct {
	Table   string
	Modules []string
}

func (e *TableOwners) Error() string {
	return "module: table " + e.Table + " is owned by " + strings.Join(e.Modules, " and ")
}

type PackageOwners struct {
	Package string
	Modules []string
}

func (e *PackageOwners) Error() string {
	return "module: package " + e.Package + " is claimed by " + strings.Join(e.Modules, " and ")
}

type UnknownConnectionKind struct {
	Module string
	Kind   connections.Kind
}

func (e *UnknownConnectionKind) Error() string {
	return "module: " + e.Module + " needs the connection kind " + string(e.Kind) + ", which no module declares"
}

func checkContract(decls []Declaration) error {
	kinds := map[connections.Kind]bool{}
	for _, d := range decls {
		for _, c := range d.Connections {
			kinds[c.Kind] = true
		}
	}
	tables := map[string]string{}
	packages := map[string]string{}
	for _, d := range decls {
		for _, n := range d.Needs {
			if !kinds[n.Kind] {
				return &UnknownConnectionKind{Module: d.Name, Kind: n.Kind}
			}
		}
		for _, t := range d.Tables {
			if strings.TrimSpace(t) == "" {
				return &InvalidDeclaration{Module: d.Name, Field: "tables"}
			}
			if prev, ok := tables[t]; ok {
				return &TableOwners{Table: t, Modules: []string{prev, d.Name}}
			}
			tables[t] = d.Name
		}
		for _, p := range d.Packages {
			if p != path.Clean(p) || !strings.HasPrefix(p, "internal/") {
				return &InvalidDeclaration{Module: d.Name, Field: "packages"}
			}
			if prev, ok := packages[p]; ok {
				return &PackageOwners{Package: p, Modules: []string{prev, d.Name}}
			}
			packages[p] = d.Name
		}
		if d.Core && len(d.Packages) > 0 {
			return &InvalidCore{Module: d.Name, Reason: "owns the platform packages and claims none"}
		}
	}
	return nil
}
