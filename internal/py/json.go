package py

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

type Pair struct {
	Key   string
	Value any
}

// Dict is a Python dict that dumps in insertion order.
type Dict []Pair

// MarshalJSON is JSONDumps(d), so encoding/json keeps the insertion order.
func (d Dict) MarshalJSON() ([]byte, error) { return []byte(JSONDumps(d)), nil }

// JSONOpts selects json.dumps arguments. The zero value is the defaults: ", " and ": " separators, non-ASCII
// escaped, keys in insertion order. NoEnsureASCII is ensure_ascii=False, Compact is separators=(",", ":"),
// SortKeys is sort_keys=True, Indent is indent=N (items on their own lines, separated by "," alone).
type JSONOpts struct {
	NoEnsureASCII bool
	Compact       bool
	SortKeys      bool
	Indent        int
}

// JSONDumps is json.dumps(v, ...), the one JSON encoder: v is nil, a bool, an integer, a float64, a *big.Int, a
// string, a []string, an []int, []int64 or []uint32, a []any, a Dict or a map[string]any (dumped with sorted keys, Go maps being unordered).
// It panics on any other type.
func JSONDumps(v any, opts ...JSONOpts) string {
	var o JSONOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	var b strings.Builder
	dump(&b, v, o)
	return b.String()
}

// JSONRedump is json.dumps(json.loads(raw), ...): JSON text spelled as Python would write it, keys in their
// original order.
func JSONRedump(raw []byte, opts ...JSONOpts) (string, error) {
	v, err := JSONLoads(raw)
	if err != nil {
		return "", err
	}
	return JSONDumps(v, opts...), nil
}

func dump(b *strings.Builder, v any, o JSONOpts) { dumpAt(b, v, o, 0) }

// container writes the n elements of an array or object, each through each(i), as json.dumps lays them out.
func container(b *strings.Builder, open, closer byte, n int, o JSONOpts, level int, each func(i int)) {
	itemSep := ", "
	if o.Compact || o.Indent > 0 {
		itemSep = ","
	}
	b.WriteByte(open)
	for i := range n {
		if i > 0 {
			b.WriteString(itemSep)
		}
		if o.Indent > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", o.Indent*(level+1)))
		}
		each(i)
	}
	if n > 0 && o.Indent > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", o.Indent*level))
	}
	b.WriteByte(closer)
}

func anySlice[T any](v []T) []any {
	out := make([]any, len(v))
	for i, e := range v {
		out[i] = e
	}
	return out
}

func dumpAt(b *strings.Builder, v any, o JSONOpts, level int) {
	keySep, itemSep := ": ", ", "
	if o.Compact {
		keySep, itemSep = ":", ","
	}
	if o.Indent > 0 {
		switch s := v.(type) {
		case []string:
			v = anySlice(s)
		case []int:
			v = anySlice(s)
		case []int64:
			v = anySlice(s)
		case []uint32:
			v = anySlice(s)
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
		container(b, '[', ']', len(v), o, level, func(i int) { dumpString(b, v[i], o) })
	case []int:
		dumpInts(b, v, itemSep)
	case []int64:
		dumpInts(b, v, itemSep)
	case []uint32:
		dumpInts(b, v, itemSep)
	case []any:
		container(b, '[', ']', len(v), o, level, func(i int) { dumpAt(b, v[i], o, level+1) })
	case map[string]any:
		pairs := make(Dict, 0, len(v))
		for k, e := range v {
			pairs = append(pairs, Pair{k, e})
		}
		slices.SortFunc(pairs, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		dumpAt(b, pairs, o, level)
	case Dict:
		if o.SortKeys {
			v = slices.Clone(v)
			slices.SortStableFunc(v, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		}
		container(b, '{', '}', len(v), o, level, func(i int) {
			dumpString(b, v[i].Key, o)
			b.WriteString(keySep)
			dumpAt(b, v[i].Value, o, level+1)
		})
	default:
		panic(fmt.Sprintf("py: JSONDumps of %T", v))
	}
}

func dumpString(b *strings.Builder, s string, o JSONOpts) {
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
