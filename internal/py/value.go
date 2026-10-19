package py

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// FloatRepr is repr(float).
func FloatRepr(f float64) string {
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

// ReprValue is repr of a decoded JSON value: nil, bool, int, int64, *big.Int, float64, string, []string, []any,
// Dict or map[string]any (in key order).
func ReprValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case *big.Int:
		return v.String()
	case float64:
		return FloatRepr(v)
	case string:
		return Repr(v)
	case []string:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = Repr(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = ReprValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Dict:
		parts := make([]string, len(v))
		for i, p := range v {
			parts[i] = Repr(p.Key) + ": " + ReprValue(p.Value)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = Repr(k) + ": " + ReprValue(v[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	panic(fmt.Sprintf("py: ReprValue of %T", v))
}

// TypeName is type(v).__name__ of a decoded JSON value or a Go value standing for one.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int64, uint32, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []string, []any, []int, []int64, []uint32:
		return "list"
	case Dict, map[string]any:
		return "dict"
	}
	panic(fmt.Sprintf("py: TypeName of %T", v))
}

// Get is d.get(key): the value of the last pair named key.
func (d Dict) Get(key string) (any, bool) {
	for i := len(d) - 1; i >= 0; i-- {
		if d[i].Key == key {
			return d[i].Value, true
		}
	}
	return nil, false
}

// Set is d[key] = value: it replaces the pair in place or appends one.
func (d Dict) Set(key string, value any) Dict {
	out := slices.Clone(d)
	for i := range out {
		if out[i].Key == key {
			out[i].Value = value
			return out
		}
	}
	return append(out, Pair{key, value})
}

// JSONError is Python's json.JSONDecodeError: Msg is the bare message, Pos the offset of the error in code
// points, Line and Col its 1-based line and column, as the C scanner of CPython 3.14 reports them.
type JSONError struct {
	Msg       string
	Pos       int
	Line, Col int
}

func (e JSONError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Line, e.Col, e.Pos)
}

// PyClass is the exception class name Python reports.
func (JSONError) PyClass() string { return "JSONDecodeError" }

// PyModule is the module Python reports for the class.
func (JSONError) PyModule() string { return "json.decoder" }

// jsonFail builds the JSONError for the byte offset pos of doc.
func jsonFail(msg, doc string, pos int) JSONError {
	pos = min(pos, len(doc))
	head := doc[:pos]
	cp := utf8.RuneCountInString(head)
	line := strings.Count(head, "\n") + 1
	col := cp + 1
	if nl := strings.LastIndexByte(head, '\n'); nl >= 0 {
		col = utf8.RuneCountInString(head[nl:])
	}
	return JSONError{Msg: msg, Pos: cp, Line: line, Col: col}
}

func (p *jsonParser) fail(msg string, pos int) JSONError { return jsonFail(msg, p.s, pos) }

// JSONLoads is json.loads: objects become Dict (a duplicate key keeps its first position and its last value),
// arrays []any, integers int64 or *big.Int, other numbers float64 (NaN and the infinities included).
// A lone surrogate escape becomes U+FFFD: a Go string cannot hold it.
func JSONLoads(data []byte) (any, error) {
	p := &jsonParser{s: string(data)}
	if strings.HasPrefix(p.s, "\ufeff") {
		return nil, p.fail("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i < len(p.s) {
		return nil, p.fail("Extra data", p.i)
	}
	return v, nil
}

type jsonParser struct {
	s string
	i int
}

func (p *jsonParser) space() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *jsonParser) lit(word string) bool {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *jsonParser) value(depth int) (any, error) {
	if depth > 900 {
		return nil, errors.New("maximum recursion depth exceeded")
	}
	if p.i >= len(p.s) {
		return nil, p.fail("Expecting value", p.i)
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case p.lit("null"):
		return nil, nil
	case p.lit("true"):
		return true, nil
	case p.lit("false"):
		return false, nil
	case p.lit("NaN"):
		return math.NaN(), nil
	case p.lit("Infinity"):
		return math.Inf(1), nil
	case p.lit("-Infinity"):
		return math.Inf(-1), nil
	case c == '-' || c >= '0' && c <= '9':
		return p.num()
	}
	return nil, p.fail("Expecting value", p.i)
}

func (p *jsonParser) num() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		p.i = start
		return nil, p.fail("Expecting value", start)
	}
	save := p.i
	if p.i+1 < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if digits() == 0 {
			p.i = save
		}
	}
	save = p.i
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			p.i = save
		}
	}
	return number(p.s[start:p.i])
}

func (p *jsonParser) str() (any, error) {
	begin := p.i
	p.i++
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return nil, p.fail("Unterminated string starting at", begin)
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c < 0x20:
			return nil, p.fail("Invalid control character at", p.i)
		case c != '\\':
			b.WriteByte(c)
			p.i++
			continue
		}
		esc := p.i
		p.i++
		if p.i >= len(p.s) {
			return nil, p.fail("Unterminated string starting at", begin)
		}
		e := p.s[p.i]
		p.i++
		if i := strings.IndexByte("\"\\/bfnrt", e); i >= 0 {
			b.WriteByte("\"\\/\b\f\n\r\t"[i])
			continue
		}
		if e != 'u' {
			return nil, p.fail("Invalid \\escape", esc)
		}
		r, ok := p.hex4()
		if !ok {
			return nil, p.fail("Invalid \\uXXXX escape", esc+1)
		}
		if r >= 0xD800 && r < 0xDC00 && strings.HasPrefix(p.s[p.i:], "\\u") {
			save := p.i
			p.i += 2
			lo, ok := p.hex4()
			if !ok {
				return nil, p.fail("Invalid \\uXXXX escape", save+1)
			}
			if c := utf16.DecodeRune(r, lo); c != unicode.ReplacementChar {
				r = c
			} else {
				p.i = save
			}
		}
		b.WriteRune(r)
	}
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	n, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
	if err != nil || strings.ContainsAny(p.s[p.i:p.i+4], "+-_") {
		return 0, false
	}
	p.i += 4
	return rune(n), true
}

func (p *jsonParser) array(depth int) (any, error) {
	p.i++
	out := []any{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return out, nil
	}
	for {
		p.space()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.space()
		switch {
		case p.i < len(p.s) && p.s[p.i] == ',':
			comma := p.i
			p.i++
			p.space()
			if p.i < len(p.s) && p.s[p.i] == ']' {
				return nil, p.fail("Illegal trailing comma before end of array", comma)
			}
		case p.i < len(p.s) && p.s[p.i] == ']':
			p.i++
			return out, nil
		default:
			return nil, p.fail("Expecting ',' delimiter", p.i)
		}
	}
}

func (p *jsonParser) object(depth int) (any, error) {
	p.i++
	out := Dict{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return out, nil
	}
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.fail("Expecting property name enclosed in double quotes", p.i)
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.fail("Expecting ':' delimiter", p.i)
		}
		p.i++
		p.space()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = out.Set(k.(string), v)
		p.space()
		switch {
		case p.i < len(p.s) && p.s[p.i] == ',':
			comma := p.i
			p.i++
			p.space()
			if p.i < len(p.s) && p.s[p.i] == '}' {
				return nil, p.fail("Illegal trailing comma before end of object", comma)
			}
		case p.i < len(p.s) && p.s[p.i] == '}':
			p.i++
			return out, nil
		default:
			return nil, p.fail("Expecting ',' delimiter", p.i)
		}
	}
}

func number(text string) (any, error) {
	if !strings.ContainsAny(text, ".eE") {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
		n, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, fmt.Errorf("bad number %q", text)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(f, 0) {
		return nil, err
	}
	return f, nil
}

// Truthy is bool(v) of a decoded JSON value.
func Truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case *big.Int:
		return v.Sign() != 0
	case []any:
		return len(v) > 0
	case []string:
		return len(v) > 0
	case Dict:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}
