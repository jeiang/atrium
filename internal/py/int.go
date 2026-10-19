package py

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
)

// Exception is a Python built-in exception (ValueError, TypeError, ...) raised by an emulated builtin; its
// text is the exception's str().
type Exception struct{ Class, Msg string }

func (e *Exception) Error() string { return e.Msg }

func (e *Exception) PyClass() string { return e.Class }

func raise(class, format string, args ...any) error {
	return &Exception{class, fmt.Sprintf(format, args...)}
}

// Int is int(v) for a decoded JSON value or a Go integer: bools are 0 and 1, floats truncate, strings parse
// as base 10 the way int(str) does.
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

// IntString is int(s) in base 10: surrounding whitespace, one sign, Unicode decimal digits and single
// underscores between digits.
func IntString(s string) (*big.Int, error) {
	invalid := raise("ValueError", "invalid literal for int() with base 10: %s", Repr(s))
	body := Strip(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	n := new(big.Int)
	ten := big.NewInt(10)
	digits, underscore := 0, false
	for _, r := range body {
		switch {
		case r == '_' && digits > 0 && !underscore:
			underscore = true
		case IsDecimal(r):
			n.Mul(n, ten).Add(n, big.NewInt(int64(DigitValue(r))))
			digits++
			underscore = false
		default:
			return nil, invalid
		}
	}
	if digits == 0 || underscore {
		return nil, invalid
	}
	if neg {
		n.Neg(n)
	}
	return n, nil
}

// Iter is list(v): the items of a list, the characters of a str, the keys of a dict.
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

// AttributeError is the AttributeError of reading attr on v.
func AttributeError(v any, attr string) error {
	return raise("AttributeError", "'%s' object has no attribute '%s'", TypeName(v), attr)
}

// TypeError is a TypeError with the given text.
func TypeError(format string, args ...any) error { return raise("TypeError", format, args...) }

// LaxInt is how pydantic reads a str for an int field in lax mode, as FastAPI does for a query, path or form
// value: surrounding whitespace is stripped, then one sign, ASCII digits with single underscores between digits,
// and optionally "." followed by one or more zeros; anything else, including exponents and Unicode digits, is
// refused (ok is false).
func LaxInt(s string) (n *big.Int, ok bool) {
	body := Strip(s)
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

// LaxInt64 is LaxInt as an int64; ok is also false when the value does not fit.
func LaxInt64(s string) (int64, bool) {
	n, ok := LaxInt(s)
	if !ok || !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}
