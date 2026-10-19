package py

import (
	"fmt"
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

// JSONDumps is json.dumps(v) with its defaults: ", " and ": " separators and every non-ASCII character
// escaped. v is nil, a bool, an integer, a string, a []string, a []any or a Dict.
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
	case string:
		dumpString(b, v)
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
