package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/tools/internal/units"
)

type exception struct{ from, to, slice string }

func unitEdges() ([]string, error) {
	set, err := units.Load()
	if err != nil {
		return nil, err
	}
	allowedEdge := map[[2]string]string{}
	for _, e := range exceptions {
		key := [2]string{e.from, e.to}
		if _, dup := allowedEdge[key]; dup {
			return nil, &duplicateException{e}
		}
		allowedEdge[key] = e.slice
	}
	used := map[[2]string]bool{}
	var edges []string
	fset := token.NewFileSet()
	err = filepath.WalkDir("internal", func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file = filepath.ToSlash(file)
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		from := path.Dir(file)
		if units.CompositionRoot(from) {
			return nil
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		own, _, inUnit := set.Of(from)
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			to, ok := strings.CutPrefix(imp, units.Module+"/")
			if !ok {
				continue
			}
			other, root, ok := set.Of(to)
			if !ok || (inUnit && other.Name == own.Name) {
				continue
			}
			contract := to == root+"/contract" || strings.HasPrefix(to, root+"/contract/")
			if inUnit && contract && slices.Contains(own.Depends, other.Name) {
				continue
			}
			key := [2]string{from, to}
			if _, ok := allowedEdge[key]; ok {
				used[key] = true
				continue
			}
			switch {
			case !inUnit:
				edges = append(edges, "platform package imports the unit "+other.Name+": "+file+"\t"+imp)
			case contract:
				edges = append(edges, "unit "+own.Name+" imports the contract of "+other.Name+" without declaring the dependency: "+file+"\t"+imp)
			default:
				edges = append(edges, "unit "+own.Name+" imports a package of "+other.Name+" outside its contract: "+file+"\t"+imp)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, e := range exceptions {
		if !used[[2]string{e.from, e.to}] {
			edges = append(edges, "exception names no import, remove it: "+e.from+" -> "+e.to+" ("+e.slice+")")
		}
	}
	return edges, nil
}

type duplicateException struct{ e exception }

func (d *duplicateException) Error() string {
	return "exception listed twice: " + d.e.from + " -> " + d.e.to
}
