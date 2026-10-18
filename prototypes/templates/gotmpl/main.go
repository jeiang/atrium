// PROTOTYPE (#75, throwaway): Jinja templates ported to stdlib html/template.
// `go run ./gotmpl` (from prototypes/templates) writes out/gotmpl-thread.html and out/gotmpl-proposals.html.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"strings"
)

//go:embed templates/*.gohtml
var tplFS embed.FS

// sharedFiles holds every file except the pages; base.gohtml is the layout (the "extends" target).
var sharedFiles = []string{"templates/_*.gohtml", "templates/sidebar.gohtml", "templates/toast.gohtml", "templates/base.gohtml"}

// build parses one page set: shared files + base layout + the page that overrides the blocks.
// One full parse per page instead of Clone(), because `partial` needs a pointer to the final set.
func build(page string) (*template.Template, error) {
	var root *template.Template
	root = template.New("root").Funcs(funcMap(func() *template.Template { return root }))
	if os.Getenv("GOTMPL_STRICT") != "" { // experiment: typos in data keys become errors (but optional keys then need `haskey`)
		root.Option("missingkey=error")
	}
	if _, err := root.ParseFS(tplFS, sharedFiles...); err != nil {
		return nil, err
	}
	// page last: its {{define "title"}} etc. replace the empty {{block}} defaults from base.gohtml.
	if _, err := root.ParseFS(tplFS, "templates/"+page+".gohtml"); err != nil {
		return nil, err
	}
	return root, nil
}

// normalise turns json.Number into int64/float64 so `eq`, `gt`, `printf "%d"` behave like Python ints/floats.
func normalise(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalise(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalise(e)
		}
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			f, _ := x.Float64()
			return f
		}
		i, _ := x.Int64()
		return i
	}
	return v
}

func render(page string, ctx map[string]any) ([]byte, error) {
	root, err := build(page)
	if err != nil {
		return nil, err
	}
	if _, ok := ctx["mode"]; !ok {
		ctx["mode"] = "live"
	}
	var b bytes.Buffer
	if err := root.ExecuteTemplate(&b, "base.gohtml", ctx); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func main() {
	raw, err := os.ReadFile("data.json")
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		panic(err)
	}
	normalise(data)
	for _, name := range []string{"thread", "proposals"} {
		out, err := render(name, data[name].(map[string]any))
		if err != nil {
			fmt.Fprintln(os.Stderr, name, "ERROR:", err)
			os.Exit(1)
		}
		if err := os.WriteFile("out/gotmpl-"+name+".html", out, 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", name, len(out))
	}
}
