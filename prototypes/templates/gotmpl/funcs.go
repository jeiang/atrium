package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"reflect"
	"strings"
	texttemplate "text/template"

	"prototype/templates/shared"
)

// funcMap holds the Jinja filters/globals/tests. getSet returns the per-page template set so that
// `partial` (the {% set x %}...{% endset %} / caller() capture mechanism) can execute named templates.
func funcMap(getSet func() *template.Template) template.FuncMap {
	return template.FuncMap{
		// --- globals from the Jinja environment
		"csrf_token":    func() string { return "csrf-fake" },
		"asset_version": func() string { return "proto" },
		"accounts":      func() []string { return []string{"icloud", "gmail"} },

		// --- constructors (Jinja dict / list literals)
		"dict": dict,
		"list": func(v ...any) []any { return v },
		// ctx copies a map and adds/overrides keys: emulates {% include %} / {% with %} / macro closures.
		"ctx": func(base map[string]any, kv ...any) (map[string]any, error) {
			out := make(map[string]any, len(base)+len(kv)/2)
			for k, v := range base {
				out[k] = v
			}
			add, err := dict(kv...)
			for k, v := range add {
				out[k] = v
			}
			return out, err
		},

		// --- macros that are really functions
		"icon": icon,
		"partial": func(name string, data any) (template.HTML, error) {
			var b bytes.Buffer
			if err := getSet().ExecuteTemplate(&b, name, data); err != nil {
				return "", err
			}
			return template.HTML(b.String()), nil // output of an auto-escaped template: safe to mark
		},

		// --- filters
		"markdown": func(v any) template.HTML { return template.HTML(shared.Markdown(str(v))) },
		"ts": func(v any, style string) string {
			f, _ := toFloat(v)
			return shared.TS(f, style)
		},
		"truncate":   func(n int, v any) string { return shared.Truncate(str(v), n) },
		"filesize":   func(v any) string { f, _ := toFloat(v); return shared.FileSizeFormat(int64(f)) },
		"urlencode":  func(v any) string { return shared.URLEncode(str(v)) },
		"lower":      func(v any) string { return strings.ToLower(str(v)) },
		"replace":    func(old, new string, v any) string { return strings.ReplaceAll(str(v), old, new) },
		"join":       func(sep string, l any) string { return strings.Join(strs(l), sep) },
		"round":      func(v any) int { f, _ := toFloat(v); return shared.Round(f) },
		"fdiv":       func(a, b any) float64 { x, _ := toFloat(a); y, _ := toFloat(b); return x / y },
		"fmul":       func(a, b any) float64 { x, _ := toFloat(a); y, _ := toFloat(b); return x * y },
		"idiv":       func(a, b any) int64 { x, _ := toFloat(a); y, _ := toFloat(b); return int64(x) / int64(y) },
		"add":        func(a, b any) int64 { x, _ := toFloat(a); y, _ := toFloat(b); return int64(x + y) },
		"last":       last,
		"selattr":    selattr,
		"mapattr":    mapattr,
		"sumlens":    sumlens,
		"countwhere": countwhere,
		"tojson": func(v any) (string, error) { // tojson | attribute: html/template escapes the result for the attr
			b, err := json.Marshal(v)
			return string(b), err
		},

		// --- tests / helpers
		"ternary": func(c any, a, b any) any {
			if ok, _ := texttemplate.IsTrue(c); ok {
				return a
			}
			return b
		},
		"isnil": func(v any) bool { return v == nil },
		"blank": func(v any) bool { return v == nil || v == "" },
		"isslice": func(v any) bool {
			if v == nil {
				return false
			}
			k := reflect.TypeOf(v).Kind()
			return k == reflect.Slice || k == reflect.Array
		},
		"haskey": func(m any, k string) bool {
			mm, ok := m.(map[string]any)
			if !ok {
				return false
			}
			_, ok = mm[k]
			return ok
		},
		"get": func(m any, k, def any) any {
			if mm, ok := m.(map[string]any); ok {
				if v, ok := mm[str(k)]; ok {
					return v
				}
			}
			return def
		},

		// --- constant dicts (STAGE_LABELS etc.)
		"lbl": func(set string, key any, def ...any) any {
			for _, p := range labelSets[set] {
				if p.Key == str(key) {
					return p.Label
				}
			}
			if len(def) > 0 {
				return def[0]
			}
			return key
		},
		"labels":    func(set string) []pair { return labelSets[set] },
		"split2":    func(s any) []string { return strings.SplitN(str(s), "|", 2) },
		"hasprefix": func(s any, p string) bool { return strings.HasPrefix(str(s), p) },
	}
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

func icon(name string, class ...string) (template.HTML, error) {
	paths, ok := icons[name]
	if !ok {
		return "", fmt.Errorf("icon %q not found", name) // Jinja silently renders an empty svg body
	}
	c := ""
	if len(class) > 0 {
		c = class[0]
	}
	return template.HTML(`<svg class="lucide lucide-` + html.EscapeString(name) + ` ` + html.EscapeString(c) +
		`" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">` +
		string(paths) + `</svg>`), nil
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case template.HTML:
		return string(x)
	}
	return fmt.Sprint(v)
}

func strs(l any) []string {
	rv := reflect.ValueOf(l)
	if l == nil || rv.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, rv.Len())
	for i := range out {
		out[i] = str(rv.Index(i).Interface())
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func last(l any) any {
	rv := reflect.ValueOf(l)
	if l == nil || rv.Kind() != reflect.Slice || rv.Len() == 0 {
		return nil
	}
	return rv.Index(rv.Len() - 1).Interface()
}

func elems(l any) []map[string]any {
	var out []map[string]any
	if rv := reflect.ValueOf(l); l != nil && rv.Kind() == reflect.Slice {
		for i := range rv.Len() {
			if m, ok := rv.Index(i).Interface().(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func truthy(v any) bool { ok, _ := texttemplate.IsTrue(v); return ok }

// selattr: `| selectattr("key")`.
func selattr(key string, l any) []any {
	var out []any
	for _, m := range elems(l) {
		if truthy(m[key]) {
			out = append(out, m)
		}
	}
	return out
}

// mapattr: `| map(attribute="key")`.
func mapattr(key string, l any) []any {
	var out []any
	for _, m := range elems(l) {
		out = append(out, m[key])
	}
	return out
}

// sumlens: `| map(attribute=key) | map("length") | sum`.
func sumlens(key string, l any) int {
	n := 0
	for _, m := range elems(l) {
		if rv := reflect.ValueOf(m[key]); m[key] != nil && rv.Kind() == reflect.Slice {
			n += rv.Len()
		}
	}
	return n
}

// countwhere: `| selectattr("key", "equalto", val) | list | length`.
func countwhere(l any, key string, val any) int {
	n := 0
	for _, m := range elems(l) {
		if m[key] == val {
			n++
		}
	}
	return n
}
