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

type Dict []Pair

func (d Dict) MarshalJSON() ([]byte, error) { return []byte(JSONDumps(d)), nil }

type JSONOpts struct {
	NoEnsureASCII bool
	Compact       bool
	SortKeys      bool
	Indent        int
	level         int
}

func (o JSONOpts) nested() JSONOpts {
	o.level++
	return o
}

func (o JSONOpts) newline(b *strings.Builder) {
	if o.Indent > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", o.Indent*o.level))
	}
}

func JSONDumps(v any, opts ...JSONOpts) string {
	var o JSONOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	var b strings.Builder
	dump(&b, v, o)
	return b.String()
}

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
		inner := o.nested()
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
		inner := o.nested()
		for i, p := range v {
			if i > 0 {
				b.WriteString(itemSep)
			}
			inner.newline(b)
			dumpString(b, p.Key, o)
			b.WriteString(keySep)
			dump(b, p.Value, inner)
		}
		if len(v) > 0 {
			o.newline(b)
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
