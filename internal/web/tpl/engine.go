package tpl

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
)

//go:embed templates/*.gohtml templates/basecoat/*.gohtml
var builtin embed.FS

const layout = "base.gohtml"

type Config struct {
	AssetVersion string
	Globals      map[string]any
	Extra        fs.FS
	Modules      []fs.FS
}

type Toast struct {
	Category    string
	Title       string
	Description string
}

type Frame struct {
	CSRF         string
	Mode         string
	Paused       bool
	PendingCount int
	Page         string
	Toast        *Toast
	Data         any
}

type ErrorView struct {
	Status  int    `json:"status"`
	Problem string `json:"problem"`
	Path    string `json:"path"`
}

type Engine struct {
	globals      map[string]any
	assetVersion string
	shared       *template.Template
	pages        map[string]*template.Template
}

func AssetURL(version, path string) string {
	return "/static/" + path + "?v=" + version
}

func New(cfg Config) (*Engine, error) {
	e := &Engine{
		globals:      maps.Clone(cfg.Globals),
		assetVersion: cfg.AssetVersion,
		pages:        map[string]*template.Template{},
	}
	sources, err := collect(cfg)
	if err != nil {
		return nil, err
	}
	if e.shared, err = e.build(sources, ""); err != nil {
		return nil, err
	}
	for page := range sources.pages {
		set, err := e.build(sources, page)
		if err != nil {
			return nil, err
		}
		e.pages[page] = set
	}
	return e, nil
}

func (e *Engine) Pages() []string {
	return slices.Sorted(maps.Keys(e.pages))
}

func (e *Engine) Fragments() []string {
	var names []string
	for _, t := range e.shared.Templates() {
		if strings.HasPrefix(t.Name(), "_") && strings.HasSuffix(t.Name(), ".gohtml") {
			names = append(names, t.Name())
		}
	}
	slices.Sort(names)
	return names
}

func (e *Engine) Page(w io.Writer, name string, f *Frame) error {
	set, ok := e.pages[name]
	if !ok {
		return fmt.Errorf("tpl: no page %q", name)
	}
	return execute(w, set, layout, f)
}

func (e *Engine) Fragment(w io.Writer, name string, f *Frame) error {
	if !strings.HasPrefix(name, "_") || e.shared.Lookup(name) == nil {
		return fmt.Errorf("tpl: no fragment %q", name)
	}
	return execute(w, e.shared, name, f)
}

func execute(w io.Writer, set *template.Template, name string, f *Frame) error {
	root := *f
	root.Data = View(f.Data)
	var b bytes.Buffer
	if err := set.ExecuteTemplate(&b, name, &root); err != nil {
		return err
	}
	_, err := w.Write(b.Bytes())
	return err
}

type source struct {
	fsys fs.FS
	path string
}

type sources struct {
	shared []source
	pages  map[string]source
}

func collect(cfg Config) (sources, error) {
	out := sources{pages: map[string]source{}}
	seen := map[string]bool{}
	add := func(fsys fs.FS, dir string) error {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".gohtml") {
				continue
			}
			if seen[name] {
				return fmt.Errorf("tpl: template file %s is defined twice", name)
			}
			seen[name] = true
			src := source{fsys, path.Join(dir, name)}
			if name == layout || strings.HasPrefix(name, "_") || strings.Contains(dir, "basecoat") {
				out.shared = append(out.shared, src)
			} else {
				out.pages[name] = src
			}
		}
		return nil
	}
	for _, dir := range []string{"templates", "templates/basecoat"} {
		if err := add(builtin, dir); err != nil {
			return out, err
		}
	}
	for _, extra := range append([]fs.FS{cfg.Extra}, cfg.Modules...) {
		if extra == nil {
			continue
		}
		if err := add(extra, "."); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (e *Engine) build(s sources, page string) (*template.Template, error) {
	var root *template.Template
	root = template.New("root").Option("missingkey=error").Funcs(e.funcs(func() *template.Template { return root }))
	files := slices.Clone(s.shared)
	if page != "" {
		files = append(files, s.pages[page])
	}
	for _, src := range files {
		text, err := fs.ReadFile(src.fsys, src.path)
		if err != nil {
			return nil, err
		}
		if _, err := root.New(path.Base(src.path)).Parse(string(text)); err != nil {
			return nil, err
		}
	}
	return root, nil
}
