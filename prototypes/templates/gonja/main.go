// PROTOTYPE (#75, throwaway): render the real Jinja templates with gonja v2.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nikolalohinski/gonja/v2/builtins"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"

	"prototype/templates/shared"
)

// loadLibraries works around gonja's missing lexical scoping for imported macros: `{% from X import m %}`
// binds m to the *importer's* scope and never executes X's top-level `{% set %}`s, so macros that use
// module constants (ICONS) or sibling macros (icon, preview_attrs...) fail. We execute every library
// module once into the environment's base context (= Jinja "globals"), where all macros can see them.
func loadLibraries(env *exec.Environment, cfg *config.Config, loader loaders.Loader) error {
	g := env.Context
	g.Set("csrf_token", func() string { return "csrf-fake" })
	g.Set("asset_version", "proto")
	g.Set("accounts", []any{"icloud", "gmail"})
	for _, name := range []string{"_icons.html", "_macros.html", "basecoat/sidebar.html.jinja", "basecoat/toast.html.jinja"} {
		tpl, err := exec.NewTemplate(name, cfg, loader, env)
		if err != nil {
			return err
		}
		if err := exec.NewRenderer(env, io.Discard, cfg, loader, tpl).Execute(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func newEnv() *exec.Environment {
	filters := exec.NewFilterSet(map[string]exec.FilterFunction{})
	filters.Update(builtins.Filters)
	env := &exec.Environment{
		Context:           exec.EmptyContext().Update(builtins.GlobalFunctions).Update(builtins.GlobalVariables),
		Filters:           filters,
		Tests:             builtins.Tests,
		ControlStructures: builtins.ControlStructures,
		Methods:           builtins.Methods,
	}
	// markdown: sanitised HTML, marked safe (the gonja equivalent of Markup).
	filters.Register("markdown", func(e *exec.Evaluator, in *exec.Value, p *exec.VarArgs) *exec.Value {
		if in.IsError() {
			return in
		}
		return exec.AsSafeValue(shared.Markdown(in.String()))
	})
	filters.Register("ts", func(e *exec.Evaluator, in *exec.Value, p *exec.VarArgs) *exec.Value {
		if in.IsError() {
			return in
		}
		style := "datetime"
		if len(p.Args) > 0 {
			style = p.Args[0].String()
		}
		return exec.AsValue(shared.TS(in.Float(), style))
	})
	if USE_SHARED_BUILTIN_OVERRIDES {
		override(filters)
	}
	return env
}

// set by flags in override.go if builtin output differs
var USE_SHARED_BUILTIN_OVERRIDES = true

// norm turns json.Number into int64/float64 so ints stay ints (Python json semantics).
func norm(v any) any {
	switch x := v.(type) {
	case json.Number:
		if !strings.ContainsAny(string(x), ".eE") {
			i, _ := x.Int64()
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = norm(e)
		}
	case []any:
		for i, e := range x {
			x[i] = norm(e)
		}
	}
	return v
}

func main() {
	raw, err := os.ReadFile("data.json")
	check(err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data map[string]map[string]any
	check(dec.Decode(&data))

	cfg := config.New()
	cfg.AutoEscape = true
	cfg.StrictUndefined = os.Getenv("STRICT") != ""
	loader, err := loaders.NewFileSystemLoader("gonja/templates")
	check(err)
	env := newEnv()
	check(loadLibraries(env, cfg, loader))

	for _, name := range []string{"thread", "proposals"} {
		ctx := norm(data[name]).(map[string]any)
		if _, ok := ctx["mode"]; !ok {
			ctx["mode"] = "live"
		}
		if _, ok := ctx["toast"]; !ok {
			ctx["toast"] = false // the toast() macro is now a global; shadow it so `toast is defined and toast` stays falsy
		}
		ctx["page"] = map[string]string{"thread": "chat", "proposals": "proposals"}[name] // gonja: child-level {% set page %} is lost
		tpl, err := exec.NewTemplate(name+".html", cfg, loader, env)
		check(err)
		var out bytes.Buffer
		execErr := tpl.Execute(&out, exec.NewContext(ctx))
		check(os.WriteFile("out/gonja-"+name+".html", out.Bytes(), 0o644))
		check(execErr)
		if os.Getenv("PARTIAL") != "" && name == "proposals" {
			partialDemo(ctx, cfg, loader, env)
		}
		if os.Getenv("BENCH") != "" {
			start := time.Now()
			const n = 50
			for i := 0; i < n; i++ {
				tpl, _ := exec.NewTemplate(name+".html", cfg, loader, env)
				var o bytes.Buffer
				tpl.Execute(&o, exec.NewContext(ctx))
			}
			fmt.Printf("bench %s: %v/render (parse+execute, uncached, %d iters)\n", name, time.Since(start)/n, n)
		}
		fmt.Println("wrote", name)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

// partialDemo: htmx-style fragment rendering = render an included file, or a one-off string template calling a macro.
func partialDemo(ctx map[string]any, cfg *config.Config, loader loaders.Loader, env *exec.Environment) {
	props := ctx["proposals"].([]any)
	one := map[string]any{"proposal": props[0], "mode": "live"}
	tpl, err := exec.NewTemplate("_proposal.html", cfg, loader, env)
	check(err)
	var b bytes.Buffer
	check(tpl.Execute(&b, exec.NewContext(one)))
	fmt.Println("partial _proposal.html bytes:", b.Len())
	// macro by name: a string template that calls the (global) macro
	ml, err := loaders.NewMemoryLoader(map[string]string{"/x": `{{ pending_badge(count, oob=True) }}`})
	check(err)
	tpl2, err := exec.NewTemplate("/x", cfg, ml, env)
	check(err)
	b.Reset()
	check(tpl2.Execute(&b, exec.NewContext(map[string]any{"count": 3})))
	fmt.Println("partial macro:", b.String())
}
