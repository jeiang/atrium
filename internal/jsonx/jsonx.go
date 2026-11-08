package jsonx

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Opts mirrors the formatting knobs callers used before the py removal.
type Opts struct {
	NoEnsureASCII bool
	SortKeys      bool
	Indent        int
	Compact       bool
	level         int
}

// Dumps marshals v, matching the previous Python-style formatting for maps,
// slices and primitives and using encoding/json for structs.
func Dumps(v any, opts ...Opts) string {
	var o Opts
	if len(opts) > 0 {
		o = opts[0]
	}
	if isStruct(v) {
		return marshalStruct(v, o)
	}
	var b strings.Builder
	dump(&b, v, o)
	return b.String()
}

func isStruct(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

func marshalStruct(v any, o Opts) string {
	var (
		b   []byte
		err error
	)
	if o.Indent > 0 {
		b, err = json.MarshalIndent(v, "", strings.Repeat(" ", o.Indent))
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		panic(err)
	}
	return string(b)
}

func dump(b *strings.Builder, v any, o Opts) {
	itemSep, keySep := ", ", ": "
	if o.Compact {
		itemSep, keySep = ",", ":"
	}
	if o.Indent > 0 {
		itemSep, keySep = ",", ": "
		switch list := v.(type) {
		case []string:
			v = toAny(list)
		case []int:
			v = toAny(list)
		case []int64:
			v = toAny(list)
		case []uint32:
			v = toAny(list)
		}
	}
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case int:
		b.WriteString(strconv.Itoa(v))
	case int32:
		b.WriteString(strconv.FormatInt(int64(v), 10))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(v), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(v, 10))
	case float64:
		switch {
		case math.IsNaN(v):
			b.WriteString("NaN")
		case math.IsInf(v, 1):
			b.WriteString("Infinity")
		case math.IsInf(v, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(FloatRepr(v))
		}
	case *big.Int:
		b.WriteString(v.String())
	case string:
		dumpString(b, v, o)
	case []string:
		b.WriteByte('[')
		for i, s := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			dumpString(b, s, o)
		}
		b.WriteByte(']')
	case []int:
		dumpInts(b, v, itemSep)
	case []int64:
		dumpInts(b, v, itemSep)
	case []uint32:
		dumpInts(b, v, itemSep)
	case []any:
		b.WriteByte('[')
		inner := o
		inner.level++
		for i, e := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			inner.newline(b)
			dump(b, e, inner)
		}
		if len(v) > 0 {
			o.newline(b)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		inner := o
		inner.level++
		for i, k := range keys {
			if i > 0 {
				b.WriteString(itemSep)
			}
			inner.newline(b)
			dumpString(b, k, o)
			b.WriteString(keySep)
			dump(b, v[k], inner)
		}
		if len(v) > 0 {
			o.newline(b)
		}
		b.WriteByte('}')
	default:
		b.WriteString(marshalStruct(v, o))
	}
}

func (o Opts) newline(b *strings.Builder) {
	if o.Indent > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", o.Indent*o.level))
	}
}

func dumpString(b *strings.Builder, s string, o Opts) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xf])
		case r < 0x7f || o.NoEnsureASCII:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			writeU(b, 0xd800+(r>>10))
			writeU(b, 0xdc00+(r&0x3ff))
		default:
			writeU(b, r)
		}
	}
	b.WriteByte('"')
}

func writeU(b *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	b.WriteString(`\u`)
	b.WriteByte(hex[r>>12&0xf])
	b.WriteByte(hex[r>>8&0xf])
	b.WriteByte(hex[r>>4&0xf])
	b.WriteByte(hex[r&0xf])
}

func dumpInts[T int | int64 | uint32](b *strings.Builder, v []T, itemSep string) {
	b.WriteByte('[')
	for i, n := range v {
		if i > 0 {
			b.WriteString(itemSep)
		}
		b.WriteString(strconv.FormatInt(int64(n), 10))
	}
	b.WriteByte(']')
}

func toAny[T any](v []T) []any {
	out := make([]any, len(v))
	for i, e := range v {
		out[i] = e
	}
	return out
}

// FloatRepr formats f like Python's repr.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
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

// Loads decodes JSON, keeping integers as int64 and decimals as float64.
func Loads(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return normalize(v), nil
}

func normalize(v any) any {
	switch v := v.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	case []any:
		for i := range v {
			v[i] = normalize(v[i])
		}
		return v
	case map[string]any:
		for k := range v {
			v[k] = normalize(v[k])
		}
		return v
	}
	return v
}

// Redump re-encodes raw JSON.
func Redump(raw []byte, opts ...Opts) (string, error) {
	v, err := Loads(raw)
	if err != nil {
		return "", err
	}
	return Dumps(v, opts...), nil
}

// Nullable dereferences v, returning nil for a nil pointer.
func Nullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

// Truthy reports Python-style truthiness.
func Truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}
