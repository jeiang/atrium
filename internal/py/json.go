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
// SortKeys is sort_keys=True.
type JSONOpts struct {
	NoEnsureASCII bool
	Compact       bool
	SortKeys      bool
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

func dump(b *strings.Builder, v any, o JSONOpts) {
	itemSep, keySep := ", ", ": "
	if o.Compact {
		itemSep, keySep = ",", ":"
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
		for i, e := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			dump(b, e, o)
		}
		b.WriteByte(']')
	case map[string]any:
		pairs := make(Dict, 0, len(v))
		for k, e := range v {
			pairs = append(pairs, Pair{k, e})
		}
		slices.SortFunc(pairs, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		dump(b, pairs, o)
	case Dict:
		if o.SortKeys {
			v = slices.Clone(v)
			slices.SortStableFunc(v, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		}
		b.WriteByte('{')
		for i, p := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			dumpString(b, p.Key, o)
			b.WriteString(keySep)
			dump(b, p.Value, o)
		}
		b.WriteByte('}')
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
