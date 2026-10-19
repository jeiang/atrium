package py

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
)

type Pair struct {
	Key   string
	Value any
}

// Dict is a Python dict that dumps in insertion order.
type Dict []Pair

// MarshalJSON is JSONDumps(d), so encoding/json keeps the insertion order.
func (d Dict) MarshalJSON() ([]byte, error) { return []byte(JSONDumps(d)), nil }

// JSONDumps is json.dumps(v) with its defaults: ", " and ": " separators and every non-ASCII character
// escaped. v is nil, a bool, an integer, a float64, a *big.Int, a string, a []string, an []int, []int64 or []uint32, a []any or a Dict.
func JSONDumps(v any) string {
	var b strings.Builder
	dump(&b, v)
	return b.String()
}

func dump(b *strings.Builder, v any) {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(v), 10))
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
		dumpString(b, v)
	case []int:
		dumpInts(b, v)
	case []int64:
		dumpInts(b, v)
	case []uint32:
		dumpInts(b, v)
	case []string:
		b.WriteByte('[')
		for i, s := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, s)
		}
		b.WriteByte(']')
	case []any:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, e)
		}
		b.WriteByte(']')
	case Dict:
		b.WriteByte('{')
		for i, p := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, p.Key)
			b.WriteString(": ")
			dump(b, p.Value)
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("py: JSONDumps of %T", v))
	}
}

func dumpInts[T int | int64 | uint32](b *strings.Builder, v []T) {
	b.WriteByte('[')
	for i, n := range v {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatInt(int64(n), 10))
	}
	b.WriteByte(']')
}

func dumpString(b *strings.Builder, s string) {
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
		case r >= ' ' && r <= '~':
			b.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
}
