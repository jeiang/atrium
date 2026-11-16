package units

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/app"
)

const Module = "github.com/jeiang/atrium"

type Unit struct {
	Name     string
	App      bool
	Depends  []string
	Packages []string
	Tables   []string
}

type Set []Unit

func Load() (Set, error) {
	cat, err := app.Docs()
	if err != nil {
		return nil, err
	}
	var out Set
	for _, d := range cat.Declarations() {
		out = append(out, Unit{Name: d.Name, App: d.App, Depends: d.Depends, Packages: d.Packages, Tables: d.Tables})
	}
	return out, nil
}

func (s Set) Get(name string) (Unit, bool) {
	i := slices.IndexFunc(s, func(u Unit) bool { return u.Name == name })
	if i < 0 {
		return Unit{}, false
	}
	return s[i], true
}

func (s Set) Of(pkg string) (Unit, string, bool) {
	pkg = strings.TrimPrefix(pkg, Module+"/")
	var best Unit
	root := ""
	for _, u := range s {
		for _, r := range u.Packages {
			if (pkg == r || strings.HasPrefix(pkg, r+"/")) && len(r) > len(root) {
				best, root = u, r
			}
		}
	}
	return best, root, root != ""
}

func CompositionRoot(pkg string) bool {
	pkg = strings.TrimPrefix(pkg, Module+"/")
	return pkg == "internal/app" || strings.HasPrefix(pkg, "internal/app/") || pkg == "cmd" || strings.HasPrefix(pkg, "cmd/")
}
