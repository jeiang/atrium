package py

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func Truncate(s string, length int) string {
	const end, leeway = "...", 5
	if utf8.RuneCountInString(s) <= length+leeway {
		return s
	}
	cut := Clip(s, max(length-len(end), 0))
	if i := strings.LastIndex(cut, " "); i >= 0 {
		cut = cut[:i]
	}
	return cut + end
}

func FileSizeFormat(value float64) string {
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

func URLQuote(s string) string { return quote(s, "/") }

func quote(s, safe string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~', strings.IndexByte(safe, c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func Round(x float64, ndigits int) float64 {
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

func JSONHTMLSafe(value any) (string, error) {
	var b strings.Builder
	if err := dumpReflect(&b, reflect.ValueOf(value)); err != nil {
		return "", err
	}
	return strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`, "'", `\u0027`).Replace(b.String()), nil
}

func dumpReflect(b *strings.Builder, v reflect.Value) error {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			b.WriteString("null")
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		b.WriteString("null")
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		switch f := v.Float(); {
		case math.IsNaN(f):
			b.WriteString("NaN")
		case math.IsInf(f, 1):
			b.WriteString("Infinity")
		case math.IsInf(f, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(FloatRepr(f))
		}
	case reflect.String:
		dumpString(b, v.String(), JSONOpts{})
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i := range v.Len() {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := dumpReflect(b, v.Index(i)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("py: JSON map key type %s", v.Type().Key())
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, k.String(), JSONOpts{})
			b.WriteString(": ")
			if err := dumpReflect(b, v.MapIndex(k)); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("py: cannot encode %s as JSON", v.Type())
	}
	return nil
}

func URLQuotePlus(s string) string {
	return strings.ReplaceAll(quote(s, ""), "%20", "+")
}
