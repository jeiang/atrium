package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const module = "github.com/jeiang/atrium"

var builders = []string{module + "/internal/module", module + "/internal/app"}

var registryObjects = []string{
	module + "/internal/module.Compile",
	module + "/internal/settings.Compile",
	module + "/internal/settings.NewFrom",
	module + "/internal/settings.Registry.Register",
	module + "/internal/connections.Compile",
	module + "/internal/scheduler.Compile",
	module + "/internal/proposals.NewRegistryFrom",
	module + "/internal/ledger.NewStoreWith",
	module + "/internal/assistant/registry.New",
	module + "/internal/assistant/registry.Registry.Add",
	module + "/internal/assistant/registry.Registry.Declare",
	module + "/internal/assistant/registry.Registry.Bind",
}

var registryFields = []string{
	module + "/internal/proposals.Registry.Bind",
	module + "/internal/proposals.Registry.Executors",
	module + "/internal/proposals.Registry.Dismissers",
	module + "/internal/proposals.Registry.Sync",
	module + "/internal/ledger.Store.BindEffect",
	module + "/internal/ledger.Undoer.Bind",
}

var delegates = map[string]string{
	module + "/internal/settings.Registry.Register": module + "/internal/assistant/registry.Registry.registerSetting",
}

var initExemptions = map[string]bool{
	"internal/db/open_cgo.go:17":              true,
	"internal/mail/rebalance/rebalance.go:74": true,
}

type listed struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	ImportMap  map[string]string
	DepOnly    bool
	Module     *struct{ Main bool }
	Error      *struct{ Err string }
}

type pkg struct {
	path  string
	files []*ast.File
	info  *types.Info
	types *types.Package
}

type checker struct {
	root    string
	fset    *token.FileSet
	found   []string
	inits   map[string]bool
	loaded  map[string]bool
	defined map[string]bool
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	c := &checker{root: root, fset: token.NewFileSet(), inits: map[string]bool{}, loaded: map[string]bool{}, defined: map[string]bool{}}
	pkgs, err := c.load()
	if err != nil {
		fail(err)
	}
	for _, p := range pkgs {
		c.define(p)
	}
	var stale []string
	for _, key := range slices.Concat(registryObjects, registryFields, slices.Collect(maps.Values(delegates))) {
		if !c.defined[key] {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		fail(fmt.Errorf("rules name objects that no longer exist: %s", strings.Join(stale, ", ")))
	}
	for _, p := range pkgs {
		c.check(p)
	}
	for pos := range initExemptions {
		file := pos[:strings.LastIndex(pos, ":")]
		if c.loaded[file] && !c.inits[pos] {
			c.found = append(c.found, pos+": exemption names no init function")
		}
	}
	if len(c.found) == 0 {
		return
	}
	slices.SortFunc(c.found, byPosition)
	for _, f := range c.found {
		fmt.Fprintln(os.Stderr, f)
	}
	fmt.Fprintf(os.Stderr, "registrationownership: %d registration ownership violations\n", len(c.found))
	os.Exit(1)
}

func byPosition(a, b string) int {
	af, al := position(a)
	bf, bl := position(b)
	return cmp.Or(strings.Compare(af, bf), cmp.Compare(al, bl), strings.Compare(a, b))
}

func position(s string) (string, int) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return s, 0
	}
	line, _ := strconv.Atoi(parts[1])
	return parts[0], line
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "registrationownership:", err)
	os.Exit(1)
}

func (c *checker) load() ([]*pkg, error) {
	cmd := exec.Command("go", "list", "-e", "-export", "-deps", "-json=ImportPath,Dir,GoFiles,Export,ImportMap,DepOnly,Module,Error", "./...")
	cmd.Dir = c.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, stderr.String())
	}
	exports := map[string]string{}
	var targets []listed
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var l listed
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if l.Error != nil {
			return nil, fmt.Errorf("%s: %s", l.ImportPath, l.Error.Err)
		}
		exports[l.ImportPath] = l.Export
		if !l.DepOnly && l.Module != nil && l.Module.Main {
			targets = append(targets, l)
		}
	}
	gc := importer.ForCompiler(c.fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	pkgs := make([]*pkg, 0, len(targets))
	for _, l := range targets {
		p := &pkg{path: l.ImportPath, info: &types.Info{
			Uses:       map[*ast.Ident]types.Object{},
			Defs:       map[*ast.Ident]types.Object{},
			Types:      map[ast.Expr]types.TypeAndValue{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
			Implicits:  map[ast.Node]types.Object{},
		}}
		for _, name := range l.GoFiles {
			path := filepath.Join(l.Dir, name)
			f, err := parser.ParseFile(c.fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			p.files = append(p.files, f)
			c.loaded[c.rel(path)] = true
		}
		conf := types.Config{Importer: mapped{gc, l.ImportMap}}
		tp, err := conf.Check(l.ImportPath, c.fset, p.files, p.info)
		if err != nil {
			return nil, err
		}
		p.types = tp
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

type mapped struct {
	types.Importer
	m map[string]string
}

func (i mapped) Import(path string) (*types.Package, error) {
	if to, ok := i.m[path]; ok {
		path = to
	}
	return i.Importer.Import(path)
}

func (c *checker) rel(path string) string {
	r, err := filepath.Rel(c.root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func (c *checker) at(pos token.Pos) string {
	p := c.fset.Position(pos)
	return c.rel(p.Filename) + ":" + strconv.Itoa(p.Line)
}

func (c *checker) report(pos token.Pos, format string, args ...any) {
	c.found = append(c.found, c.at(pos)+": "+fmt.Sprintf(format, args...))
}

func (c *checker) define(p *pkg) {
	scope := p.types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		c.defined[objectKey(obj)] = true
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		for m := range named.Methods() {
			c.defined[objectKey(m)] = true
		}
		if st, ok := named.Underlying().(*types.Struct); ok {
			for f := range st.Fields() {
				c.defined[fieldKey(named, f)] = true
			}
		}
	}
}

func objectKey(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	if fn, ok := obj.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			if named := namedOf(recv.Type()); named != nil {
				return obj.Pkg().Path() + "." + named.Obj().Name() + "." + fn.Name()
			}
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func fieldKey(named *types.Named, f *types.Var) string {
	if named == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + f.Name()
}

func namedOf(t types.Type) *types.Named {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := types.Unalias(t).(*types.Named)
	if named != nil {
		named = named.Origin()
	}
	return named
}

func declaring(key string) string {
	rest := strings.TrimPrefix(key, module)
	return module + rest[:strings.Index(rest, ".")]
}

func (c *checker) check(p *pkg) {
	internal := strings.HasPrefix(p.path, module+"/internal/")
	builder := slices.Contains(builders, p.path)
	for _, f := range p.files {
		if internal {
			for _, spec := range f.Imports {
				if name := p.info.PkgNameOf(spec); name != nil && name.Imported().Path() == module+"/internal/app" {
					c.report(spec.Pos(), "internal package %s imports internal/app", p.path)
				}
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "init" && fn.Body != nil {
				pos := c.at(fn.Pos())
				c.inits[pos] = true
				if !initExemptions[pos] {
					c.checkInit(p, fn)
				}
			}
			enclosing := ""
			if ok {
				enclosing = objectKey(p.info.Defs[fn.Name])
			}
			if !builder {
				c.checkRegistries(p, decl, enclosing)
			}
			c.checkHooks(p, decl)
		}
	}
}

func (c *checker) checkInit(p *pkg, fn *ast.FuncDecl) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		obj := callee(p.info, call.Fun)
		switch obj.(type) {
		case *types.Func, *types.Var:
			if obj.Pkg() != nil && obj.Pkg() != p.types {
				c.report(call.Pos(), "init calls %s from another package", objectKey(obj))
			}
		}
		return true
	})
}

func callee(info *types.Info, fun ast.Expr) types.Object {
	for {
		switch e := fun.(type) {
		case *ast.ParenExpr:
			fun = e.X
		case *ast.IndexExpr:
			fun = e.X
		case *ast.IndexListExpr:
			fun = e.X
		case *ast.Ident:
			return info.Uses[e]
		case *ast.SelectorExpr:
			return info.Uses[e.Sel]
		default:
			return nil
		}
	}
}

func (c *checker) checkRegistries(p *pkg, decl ast.Decl, enclosing string) {
	ast.Inspect(decl, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.Ident:
			if fn, ok := p.info.Uses[e].(*types.Func); ok {
				c.checkObject(p, e.Pos(), objectKey(fn), enclosing)
			}
		case *ast.CompositeLit:
			named := namedOf(p.info.Types[e].Type)
			for _, elt := range e.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok {
					if f, ok := p.info.Uses[key].(*types.Var); ok && f.IsField() {
						c.checkField(p, kv.Pos(), fieldKey(named, f))
					}
				}
			}
		}
		return true
	})
}

func fieldOwner(sel *types.Selection) types.Type {
	t := sel.Recv()
	idx := sel.Index()
	for _, i := range idx[:len(idx)-1] {
		st, ok := namedOrSelf(t).Underlying().(*types.Struct)
		if !ok {
			return t
		}
		t = st.Field(i).Type()
	}
	return t
}

func namedOrSelf(t types.Type) types.Type {
	if ptr, ok := t.(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

func (c *checker) checkObject(p *pkg, pos token.Pos, key, enclosing string) {
	if !slices.Contains(registryObjects, key) || declaring(key) == p.path || delegates[key] == enclosing {
		return
	}
	c.report(pos, "%s is used outside the module catalog", key)
}

func (c *checker) checkField(p *pkg, pos token.Pos, key string) {
	if !slices.Contains(registryFields, key) || declaring(key) == p.path || slices.Contains(builders, p.path) {
		return
	}
	c.report(pos, "%s is set outside the module catalog", key)
}

func (c *checker) checkHooks(p *pkg, decl ast.Decl) {
	ast.Inspect(decl, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok == token.DEFINE {
			return true
		}
		for _, lhs := range as.Lhs {
			c.checkTarget(p, lhs)
		}
		return true
	})
}

func (c *checker) checkTarget(p *pkg, lhs ast.Expr) {
	target := unwrap(lhs)
	var obj types.Object
	switch e := target.(type) {
	case *ast.Ident:
		obj = p.info.Uses[e]
	case *ast.SelectorExpr:
		if sel := p.info.Selections[e]; sel != nil && sel.Kind() == types.FieldVal {
			if f, ok := sel.Obj().(*types.Var); ok {
				c.checkField(p, lhs.Pos(), fieldKey(namedOf(fieldOwner(sel)), f))
			}
			return
		}
		obj = p.info.Uses[e.Sel]
	}
	v, ok := obj.(*types.Var)
	if !ok || v.IsField() || !v.Exported() || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
		return
	}
	if !strings.HasPrefix(v.Pkg().Path(), module+"/internal/") {
		return
	}
	if _, ok := v.Type().Underlying().(*types.Signature); ok {
		c.report(lhs.Pos(), "assignment to exported function variable %s", objectKey(v))
	}
}

func unwrap(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		default:
			return e
		}
	}
}
