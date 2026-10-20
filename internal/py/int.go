package py

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"unicode"
)

type Exception struct{ Class, Msg string }

func (e *Exception) Error() string { return e.Msg }

func (e *Exception) PyClass() string { return e.Class }

func raise(class, format string, args ...any) error {
	return &Exception{class, fmt.Sprintf(format, args...)}
}

func Int(v any) (*big.Int, error) {
	switch v := v.(type) {
	case bool:
		if v {
			return big.NewInt(1), nil
		}
		return big.NewInt(0), nil
	case int:
		return big.NewInt(int64(v)), nil
	case int64:
		return big.NewInt(v), nil
	case uint32:
		return big.NewInt(int64(v)), nil
	case *big.Int:
		return new(big.Int).Set(v), nil
	case float64:
		switch {
		case math.IsNaN(v):
			return nil, raise("ValueError", "cannot convert float NaN to integer")
		case math.IsInf(v, 0):
			return nil, raise("OverflowError", "cannot convert float infinity to integer")
		}
		n, _ := new(big.Float).SetFloat64(math.Trunc(v)).Int(nil)
		return n, nil
	case string:
		return IntString(v)
	}
	return nil, raise("TypeError", "int() argument must be a string, a bytes-like object or a real number, not '%s'", TypeName(v))
}

const MaxStrDigits = 4300

func IntString(s string) (*big.Int, error) {
	invalid := raise("ValueError", "invalid literal for int() with base 10: %s", Repr(s))
	body := Strip(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	var text []byte
	digits, underscore := 0, false
	for _, r := range body {
		switch {
		case r == '_' && digits > 0 && !underscore:
			underscore = true
		case IsDecimal(r):
			text = append(text, byte('0'+DigitValue(r)))
			digits++
			underscore = false
		default:
			return nil, invalid
		}
	}
	if digits == 0 || underscore {
		return nil, invalid
	}
	if digits > MaxStrDigits {
		return nil, raise("ValueError", "Exceeds the limit (%d digits) for integer string conversion: value has %d digits; use sys.set_int_max_str_digits() to increase the limit", MaxStrDigits, digits)
	}
	n, _ := new(big.Int).SetString(string(text), 10)
	if neg {
		n.Neg(n)
	}
	return n, nil
}

func Iter(v any) ([]any, error) {
	switch v := v.(type) {
	case []any:
		return v, nil
	case []string:
		return toAny(v), nil
	case []int:
		return toAny(v), nil
	case []int64:
		return toAny(v), nil
	case []uint32:
		return toAny(v), nil
	case string:
		out := make([]any, 0, len(v))
		for _, r := range v {
			out = append(out, string(r))
		}
		return out, nil
	case Dict:
		out := make([]any, len(v))
		for i, p := range v {
			out[i] = p.Key
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return toAny(keys), nil
	}
	return nil, raise("TypeError", "'%s' object is not iterable", TypeName(v))
}

func toAny[T any](items []T) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func AttributeError(v any, attr string) error {
	return raise("AttributeError", "'%s' object has no attribute '%s'", TypeName(v), attr)
}

func TypeError(format string, args ...any) error { return raise("TypeError", format, args...) }

func LaxInt(s string) (n *big.Int, ok bool) {
	body := strings.TrimFunc(s, func(r rune) bool { return unicode.Is(unicode.White_Space, r) })
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return nil, false
	}
	unsigned := strings.TrimLeft(whole, "+-")
	if len(whole)-len(unsigned) > 1 || unsigned == "" {
		return nil, false
	}
	for i := range len(unsigned) {
		c := unsigned[i]
		switch {
		case c >= '0' && c <= '9':
		case c == '_' && i > 0 && i < len(unsigned)-1 && unsigned[i-1] != '_' && unsigned[i+1] != '_':
		default:
			return nil, false
		}
	}
	n, err := IntString(whole)
	return n, err == nil
}

func LaxInt64(s string) (int64, bool) {
	n, ok := LaxInt(s)
	if !ok || !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}
