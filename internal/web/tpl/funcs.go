package tpl

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	ttemplate "text/template"
	"time"

	"github.com/jeiang/atrium/internal/py"
)

var extraFuncs = template.FuncMap{}

// registerFuncs adds template functions from a page slice's own file; call it from an init function.
func registerFuncs(funcs template.FuncMap) {
	for name, fn := range funcs {
		if _, taken := extraFuncs[name]; taken {
			panic("tpl: function " + name + " registered twice")
		}
		extraFuncs[name] = fn
	}
}

func (e *Engine) funcs(set func() *template.Template) template.FuncMap {
	funcs := template.FuncMap{
		"dict": dict,
		"list": func(v ...any) []any { return v },
		"opt":  opt,
		"partial": func(name string, data any) (template.HTML, error) {
			var b bytes.Buffer
			if err := set().ExecuteTemplate(&b, name, data); err != nil {
				return "", fmt.Errorf("partial %s: %w", name, err)
			}
			return template.HTML(b.String()), nil
		},
		"global": func(name string) (any, error) {
			v, ok := e.globals[name]
			if !ok {
				return nil, fmt.Errorf("unknown global %q", name)
			}
			return v, nil
		},
		"asset":    func(path string) string { return AssetURL(e.assetVersion, path) },
		"icon":     icon,
		"lbl":      lbl,
		"labels":   func(set string) []pair { return labelSets[set] },
		"badge":    badge,
		"fail":     func(msg string, args ...any) (string, error) { return "", fmt.Errorf(msg, args...) },
		"markdown": func(v any) template.HTML { return Markdown(str(v)) },
		"ts":       ts,
		"truncate": func(n int, v any) string { return py.Truncate(str(v), n) },
		"filesizeformat": func(v any) (string, error) {
			f, err := toFloat(v)
			return py.FileSizeFormat(f), err
		},
		"urlencode": urlencode,
		"items":     items,
		"odict":     odict,
		"get":       get,
		"tojson":    py.JSONHTMLSafe,
		"round": func(v any, digits ...int) (float64, error) {
			f, err := toFloat(v)
			n := 0
			if len(digits) > 0 {
				n = digits[0]
			}
			return py.Round(f, n), err
		},
		"int": func(v any) (int64, error) {
			f, err := toFloat(v)
			return int64(f), err
		},
		"mul": func(a, b any) (float64, error) {
			return arith(a, b, func(x, y float64) float64 { return float64(x * y) })
		},
		"fdiv":  func(a, b any) (float64, error) { return arith(a, b, func(x, y float64) float64 { return x / y }) },
		"inc":   func(i int) int { return i + 1 },
		"lower": func(v any) string { return py.Lower(str(v)) },
		"capitalize": func(v any) string {
			s := str(v)
			for i, r := range s {
				return strings.ToUpper(string(r)) + py.Lower(s[i+len(string(r)):])
			}
			return s
		},
		"replace": func(old, repl string, v any) string { return strings.ReplaceAll(str(v), old, repl) },
		"join": func(sep string, l any) string {
			if reflect.ValueOf(l).Kind() != reflect.Slice {
				return str(l)
			}
			return strings.Join(strs(l), sep)
		},
		"string":  str,
		"last":    last,
		"selattr": func(key string, l any) []any { return filter(l, key, true) },
		"rejattr": func(key string, l any) []any { return filter(l, key, false) },
		"mapattr": mapattr,
		"sumlens": sumlens,
		"countwhere": func(l any, key string, val any) int {
			n := 0
			for _, m := range elems(l) {
				if reflect.DeepEqual(m[key], val) {
					n++
				}
			}
			return n
		},
		"ternary": func(c any, a, b any) any {
			if ok, _ := ttemplate.IsTrue(c); ok {
				return a
			}
			return b
		},
		"hasprefix": func(s any, p string) bool { return strings.HasPrefix(str(s), p) },
		"present":   func(v any) bool { return v != nil && v != "" },
		"safeurl":   safeURL,
		"pct":       pct,
		"place":     place,
		"proposal":  proposal,
		"noun":      noun,
		"sender":    sender,
		"page_path": pagePath,
		"isnil":     func(v any) bool { return v == nil },
		"haskey":    haskey,
	}
	for name, fn := range extraFuncs {
		funcs[name] = fn
	}
	return funcs
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: odd number of arguments")
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

func opt(m any, key string, def any) any {
	if mm, ok := m.(map[string]any); ok {
		if v, ok := mm[key]; ok {
			return v
		}
	}
	return def
}

func haskey(m any, key string) bool {
	mm, ok := m.(map[string]any)
	if !ok {
		return false
	}
	_, ok = mm[key]
	return ok
}

func ts(v any, style ...string) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		if x == "" {
			return "", nil
		}
	}
	epoch, err := toFloat(v)
	if err != nil {
		return "", err
	}
	sec, us := py.SplitTimestamp(epoch)
	moment := time.Unix(sec, us*1000).UTC()
	kind := "datetime"
	if len(style) > 0 {
		kind = style[0]
	}
	switch kind {
	case "iso":
		if us != 0 {
			return moment.Format("2006-01-02T15:04:05.000000") + "+00:00", nil
		}
		return moment.Format("2006-01-02T15:04:05") + "+00:00", nil
	case "date":
		return moment.Format("2006-01-02"), nil
	case "datetime":
		return moment.Format("2006-01-02 15:04"), nil
	}
	return "", fmt.Errorf("ts: unknown style %q", kind)
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

func toFloat(v any) (float64, error) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Bool:
		if rv.Bool() {
			return 1, nil
		}
		return 0, nil
	case reflect.String:
		return strconv.ParseFloat(rv.String(), 64)
	}
	return 0, fmt.Errorf("not a number: %T", v)
}

func arith(a, b any, op func(x, y float64) float64) (float64, error) {
	x, err := toFloat(a)
	if err != nil {
		return 0, err
	}
	y, err := toFloat(b)
	if err != nil {
		return 0, err
	}
	return op(x, y), nil
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

func truthy(v any) bool {
	ok, _ := ttemplate.IsTrue(v)
	return ok
}

func filter(l any, key string, want bool) []any {
	var out []any
	for _, m := range elems(l) {
		if truthy(m[key]) == want {
			out = append(out, m)
		}
	}
	return out
}

func mapattr(key string, l any) []any {
	var out []any
	for _, m := range elems(l) {
		out = append(out, m[key])
	}
	return out
}

func sumlens(key string, l any) int {
	n := 0
	for _, m := range elems(l) {
		if rv := reflect.ValueOf(m[key]); m[key] != nil && rv.Kind() == reflect.Slice {
			n += rv.Len()
		}
	}
	return n
}

func pyStr(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "None", nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case float64:
		return py.FloatRepr(x), nil
	case float32:
		return py.FloatRepr(float64(x)), nil
	case string:
		return x, nil
	case template.HTML:
		return string(x), nil
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return fmt.Sprint(v), nil
	}
	return "", fmt.Errorf("urlencode: cannot quote %T", v)
}

func items(v any) ([]py.Pair, error) {
	switch x := v.(type) {
	case map[string]any:
		pairs := make([]py.Pair, 0, len(x))
		for _, k := range slices.Sorted(maps.Keys(x)) {
			pairs = append(pairs, py.Pair{Key: k, Value: x[k]})
		}
		return pairs, nil
	case py.Dict:
		return x, nil
	case []py.Pair:
		return x, nil
	}
	return nil, fmt.Errorf("items: not a mapping: %T", v)
}

func urlencode(v any) (string, error) {
	switch v.(type) {
	case map[string]any, py.Dict, []py.Pair:
		pairs, err := items(v)
		if err != nil {
			return "", err
		}
		parts := make([]string, len(pairs))
		for i, p := range pairs {
			value, err := pyStr(p.Value)
			if err != nil {
				return "", err
			}
			parts[i] = py.URLQuotePlus(p.Key) + "=" + py.URLQuotePlus(value)
		}
		return strings.Join(parts, "&"), nil
	}
	s, err := pyStr(v)
	return py.URLQuote(s), err
}

func odict(kv ...any) (py.Dict, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("odict: odd number of arguments")
	}
	out := make(py.Dict, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("odict: key %v is not a string", kv[i])
		}
		out = out.Set(k, kv[i+1])
	}
	return out, nil
}

func get(m any, key string) any {
	switch x := m.(type) {
	case map[string]any:
		return x[key]
	case py.Dict:
		v, _ := x.Get(key)
		return v
	}
	return nil
}
