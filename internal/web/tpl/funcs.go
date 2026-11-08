package tpl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	ttemplate "text/template"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/timex"
)

var extraFuncs = template.FuncMap{}

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
		"truncate": func(n int, v any) string { return truncate(str(v), n) },
		"filesizeformat": func(v any) (string, error) {
			f, err := toFloat(v)
			return fileSizeFormat(f), err
		},
		"urlencode": urlencode,
		"items":     items,
		"odict":     odict,
		"get":       get,
		"tojson":    jsonHTMLSafe,
		"round": func(v any, digits ...int) (float64, error) {
			f, err := toFloat(v)
			n := 0
			if len(digits) > 0 {
				n = digits[0]
			}
			return roundTo(f, n), err
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
		"lower": func(v any) string { return strings.ToLower(str(v)) },
		"capitalize": func(v any) string {
			s := str(v)
			for i, r := range s {
				return strings.ToUpper(string(r)) + strings.ToLower(s[i+len(string(r)):])
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
		"compact": func(items []any) []any {
			out := make([]any, 0, len(items))
			for _, item := range items {
				if item != nil {
					out = append(out, item)
				}
			}
			return out
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
	sec, us := timex.SplitTimestamp(epoch)
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
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return 0, fmt.Errorf("not a number: %T", v)
		}
		rv = rv.Elem()
	}
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
		return floatString(x), nil
	case float32:
		return floatString(float64(x)), nil
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
			parts[i] = url.QueryEscape(p.Key) + "=" + url.QueryEscape(value)
		}
		return strings.Join(parts, "&"), nil
	}
	s, err := pyStr(v)
	return urlQuote(s), err
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

func truncate(s string, length int) string {
	const end, leeway = "...", 5
	if utf8.RuneCountInString(s) <= length+leeway {
		return s
	}
	cut := clip(s, max(length-len(end), 0))
	if i := strings.LastIndex(cut, " "); i >= 0 {
		cut = cut[:i]
	}
	return cut + end
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func fileSizeFormat(value float64) string {
	if value == 1 {
		return "1 Byte"
	}
	if value < 1000 {
		return fmt.Sprintf("%d Bytes", int64(value))
	}
	prefixes := []string{"kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	unit := 1000.0
	for i, prefix := range prefixes {
		unit *= 1000
		if value < unit || i == len(prefixes)-1 {
			return strconv.FormatFloat(1000*value/unit, 'f', 1, 64) + " " + prefix
		}
	}
	return ""
}

func jsonHTMLSafe(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func roundTo(x float64, ndigits int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	if ndigits == 0 {
		return math.RoundToEven(x)
	}
	out, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', ndigits, 64), 64)
	if err != nil {
		return x
	}
	return out
}

func floatString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	exp := strconv.FormatFloat(f, 'e', -1, 64)
	at := strings.IndexByte(exp, 'e')
	e, _ := strconv.Atoi(exp[at+1:])
	if e < -4 || e >= 16 {
		return exp
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func urlQuote(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%2F", "/")
}
