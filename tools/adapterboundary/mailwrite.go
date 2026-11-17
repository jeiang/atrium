package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/tools/internal/units"
)

const (
	mailUnit     = "mail"
	mailGate     = "internal/apps/mail"
	mailGateCall = "WriteGate("
)

var mailWritePath = []string{"internal/apps/mail/applier", "internal/apps/mail/live"}

func mailWriteEdges() ([]string, error) {
	set, err := units.Load()
	if err != nil {
		return nil, err
	}
	var edges []string
	fset := token.NewFileSet()
	walk := func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file = filepath.ToSlash(file)
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		from := path.Dir(file)
		if from != mailGate && bytes.Contains(src, []byte(mailGateCall)) {
			edges = append(edges, "mail write gate called outside the Mail client: "+file)
		}
		if own, _, ok := set.Of(from); ok && own.Name == mailUnit {
			return nil
		}
		f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			rel, ok := strings.CutPrefix(imp, units.Module+"/")
			if !ok {
				continue
			}
			for _, dir := range mailWritePath {
				if rel == dir || strings.HasPrefix(rel, dir+"/") {
					edges = append(edges, "package outside Mail imports the mail write path: "+file+"\t"+imp)
				}
			}
		}
		return nil
	}
	for _, root := range []string{"internal", "cmd"} {
		if err := filepath.WalkDir(root, walk); err != nil {
			return nil, err
		}
	}
	return edges, nil
}
