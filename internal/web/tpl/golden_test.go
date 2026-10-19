package tpl

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func newEngine(t testing.TB, c tpltest.Case, extra fstest.MapFS) *Engine {
	t.Helper()
	globals := map[string]any{}
	for k, v := range c.Globals {
		globals[k] = v
	}
	version, _ := c.Globals["asset_version"].(string)
	e, err := New(Config{AssetVersion: version, Globals: globals, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func frame(c tpltest.Case, data any) *Frame {
	f := &Frame{Data: data}
	f.CSRF, _ = c.Globals["csrf_token"].(string)
	f.Mode, _ = c.Globals["mode"].(string)
	if v, ok := c.Context["mode"].(string); ok {
		f.Mode = v
	}
	f.Paused, _ = c.Context["paused"].(bool)
	f.Page, _ = c.Context["page"].(string)
	if n, ok := c.Context["pending_count"].(int64); ok {
		f.PendingCount = int(n)
	}
	if m, ok := c.Context["toast"].(map[string]any); ok {
		f.Toast = &Toast{Category: m["category"].(string), Title: m["title"].(string), Description: m["description"].(string)}
	}
	return f
}

func TestGoldenLayout(t *testing.T) {
	for _, name := range []string{"base", "base@live", "base@shadow"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			e := newEngine(t, c, fstest.MapFS{"blank.gohtml": {Data: []byte("")}})
			var out bytes.Buffer
			if err := e.Page(&out, "blank.gohtml", frame(c, nil)); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

func TestGoldenSettingsLayout(t *testing.T) {
	c := tpltest.Load(t, "settings.context.json")
	page := `{{define "title"}} · Settings{{end}}{{define "heading"}}Settings · {{end}}{{define "content"}}{{template "settings_tabs" "usage"}}{{end}}`
	e := newEngine(t, c, fstest.MapFS{"settings.gohtml": {Data: []byte(page)}})
	f := frame(c, nil)
	f.Page = "settings"
	var out bytes.Buffer
	if err := e.Page(&out, "settings.gohtml", f); err != nil {
		t.Fatal(err)
	}
	tpltest.Compare(t, tpltest.Want(t, "settings.html"), out.String())
}

func TestGoldenFragments(t *testing.T) {
	for _, name := range []string{"_toast", "_toast@warning", "_error", "_error@404"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			e := newEngine(t, c, nil)
			var out bytes.Buffer
			if err := e.Fragment(&out, strings.SplitN(name, "@", 2)[0]+".gohtml", frame(c, errorView(c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String())
		})
	}
}

var errorRewrites = map[string][]tpltest.Rewrite{
	"error": {{
		From: `href="/triage/"><script>alert(1)</script>"`,
		To:   `href="/triage/%22%3e%3cscript%3ealert%281%29%3c/script%3e"`,
		Why:  "html/template percent-encodes unsafe characters in a URL attribute",
	}},
	"error@protocol-relative": {{
		From: `href="//evil.example/x"`,
		To:   `href="#"`,
		Why:  "safeurl rejects scheme-relative URLs",
	}},
}

func TestGoldenErrorPage(t *testing.T) {
	for _, name := range []string{"error", "error@404", "error@protocol-relative"} {
		t.Run(name, func(t *testing.T) {
			c := tpltest.Load(t, name+".context.json")
			e := newEngine(t, c, nil)
			var out bytes.Buffer
			if err := e.Page(&out, "error.gohtml", frame(c, errorView(c))); err != nil {
				t.Fatal(err)
			}
			tpltest.Compare(t, tpltest.Want(t, name+".html"), out.String(), errorRewrites[name]...)
		})
	}
}

func errorView(c tpltest.Case) ErrorView {
	status, _ := c.Context["status"].(int64)
	problem, _ := c.Context["problem"].(string)
	path, _ := c.Context["path"].(string)
	return ErrorView{Status: int(status), Problem: problem, Path: path}
}

type macroSpec struct {
	dict   []string
	single bool
	expr   string
	extra  func(c tpltest.Case) map[string]any
}

func csrf(c tpltest.Case) map[string]any { return map[string]any{"csrf": c.Globals["csrf_token"]} }

var macroSpecs = map[string]macroSpec{
	"mode_badge":     {dict: []string{"mode", "paused"}},
	"mode_note":      {dict: []string{"mode", "paused"}},
	"action_badge":   {single: true},
	"stage_label":    {expr: `lbl "stage"`},
	"stage_badge":    {single: true},
	"pct":            {expr: "pct"},
	"reason":         {dict: []string{"d"}, extra: func(c tpltest.Case) map[string]any { return map[string]any{"mode": frame(c, nil).Mode} }},
	"place":          {expr: "place"},
	"proposal":       {expr: "proposal"},
	"agreement":      {single: true},
	"noun":           {expr: "noun"},
	"sender":         {expr: "sender"},
	"open_attrs":     {single: true},
	"when":           {dict: []string{"epoch", "style"}},
	"account_switch": {dict: []string{"path", "current", "label"}},
	"folder_options": {dict: []string{"folders", "selected"}},
	"empty_state":    {dict: []string{"icon_name", "title"}},
	"condition_text": {single: true},
	"spinner":        {dict: []string{"id", "label"}},
	"status_badge":   {single: true},
	"pending_badge":  {dict: []string{"count", "oob"}},
	"external":       {single: true},
	"page_path":      {expr: "page_path"},
	"page_link":      {dict: []string{"ref", "class"}},
	"run_badge":      {single: true},
	"preview_attrs":  {single: true},
	"branch_button":  {dict: []string{"thread", "seq"}, extra: csrf},
	"file_icon":      {dict: []string{"media_type", "class"}},
	"file_chips":     {dict: []string{"thread_id", "files"}},
	"attach_field":   {},
	"attach_button":  {},
	"activity_step":  {single: true},
	"chat_item":      {dict: []string{"item", "thread", "last_seq", "busy"}, extra: csrf},
	"icon":           {expr: "icon"},
	"sidebar":        {dict: nil},
	"tabs":           {dict: nil},
	"toaster":        {dict: nil},
	"toast":          {dict: nil},
}

func adapt(call tpltest.Call) {
	kw := call.Kwargs
	switch call.Macro {
	case "chat_item":
		if item, ok := call.Args[0].(map[string]any); ok {
			if text, ok := item["text"].(string); ok {
				item["text"] = strings.ReplaceAll(text, "![img](", "[img](")
			}
		}
	case "sidebar":
		if menu, ok := kw["menu"].([]any); ok {
			for _, group := range menu {
				flattenAttrs(group.(map[string]any))
			}
		}
		if caller := call.Caller; caller != nil {
			kw["body"] = *caller
		}
	case "tabs":
		if attrs, ok := kw["main_attrs"].(map[string]any); ok {
			kw["class"] = attrs["class"]
			delete(kw, "main_attrs")
		}
		if attrs, ok := kw["tablist_attrs"].(map[string]any); ok {
			kw["variant"] = attrs["data-variant"]
			kw["label"] = attrs["aria-label"]
			delete(kw, "tablist_attrs")
		}
		for _, set := range kw["tabsets"].([]any) {
			m := set.(map[string]any)
			m["tab"] = template.HTML(m["tab"].(string))
		}
	}
}

func flattenAttrs(item map[string]any) {
	if attrs, ok := item["attrs"].(map[string]any); ok {
		item["class"] = attrs["class"]
		delete(item, "attrs")
	}
	if items, ok := item["items"].([]any); ok {
		for _, child := range items {
			flattenAttrs(child.(map[string]any))
		}
	}
}

func harness(t testing.TB, c tpltest.Case) (fstest.MapFS, []any, []string) {
	t.Helper()
	var defs strings.Builder
	var data []any
	var names []string
	for i, call := range c.Calls {
		spec, ok := macroSpecs[call.Macro]
		if !ok {
			t.Fatalf("no macro spec for %s", call.Macro)
		}
		adapt(call)
		var body string
		switch {
		case spec.expr != "":
			data = append(data, call.Args)
			args := ""
			for n := range call.Args {
				args += fmt.Sprintf(" (index (index .Data.calls %d) %d)", i, n)
			}
			body = fmt.Sprintf("{{%s%s}}", spec.expr, args)
		case spec.single:
			data = append(data, call.Args)
			body = fmt.Sprintf(`{{template %q (index (index .Data.calls %d) 0)}}`, call.Macro, i)
		case call.Macro == "attach_field" || call.Macro == "attach_button":
			data = append(data, nil)
			body = fmt.Sprintf(`{{template %q}}`, call.Macro)
		default:
			args := map[string]any{}
			for n, v := range call.Args {
				args[spec.dict[n]] = v
			}
			for k, v := range call.Kwargs {
				args[k] = v
			}
			if call.Caller != nil && call.Macro == "empty_state" {
				args["body"] = *call.Caller
			}
			if spec.extra != nil {
				for k, v := range spec.extra(c) {
					args[k] = v
				}
			}
			data = append(data, args)
			body = fmt.Sprintf(`{{template %q (index .Data.calls %d)}}`, call.Macro, i)
		}
		name := fmt.Sprintf("_call_%d", i)
		names = append(names, name)
		fmt.Fprintf(&defs, "{{define %q}}\n<section data-golden-call=\"%d\" data-macro=%q>\n%s\n</section>{{end}}\n", name, i, call.Macro, body)
	}
	return fstest.MapFS{"_harness.gohtml": {Data: []byte(defs.String())}}, data, names
}

func TestGoldenMacros(t *testing.T) {
	cases := []struct {
		context string
		html    string
		rewrite []tpltest.Rewrite
	}{
		{"_macros.context.json", "_macros.html", []tpltest.Rewrite{
			{From: `javascript:alert(document.cookie)`, To: `#`, Why: "external() filters schemes outside http, https, mailto, tel and notion"},
			{From: `[bad](javascript:alert(1))`, To: `bad`, Why: "markdown-it leaves a javascript: link as literal text; goldmark parses the link and bluemonday strips it to its label"},
		}},
		{"_macros@shadow.context.json", "_macros@shadow.html", nil},
		{"_icons.context.json", "_icons.html", nil},
		{"basecoat/sidebar.context.json", "basecoat/sidebar.html", nil},
		{"basecoat/tabs.context.json", "basecoat/tabs.html", nil},
		{"basecoat/toast.context.json", "basecoat/toast.html", nil},
	}
	for _, tc := range cases {
		t.Run(tc.html, func(t *testing.T) {
			c := tpltest.Load(t, tc.context)
			extra, data, names := harness(t, c)
			e := newEngine(t, c, extra)
			var out bytes.Buffer
			f := frame(c, map[string]any{"calls": data})
			for _, name := range names {
				if err := e.Fragment(&out, name, f); err != nil {
					t.Fatal(err)
				}
			}
			tpltest.Compare(t, tpltest.Want(t, tc.html), out.String(), tc.rewrite...)
		})
	}
}
