package tool

import (
	"math"
	"math/big"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
)

// BoundedInt is bounded_int: max(minimum, min(int(value), maximum)) of args[name] or def. A bool or non-number is
// a ToolError; a float truncates; NaN and the infinities raise ValueError and OverflowError as int() does.
func BoundedInt(args py.Dict, name string, def, maximum, minimum int64) (int64, error) {
	value, ok := args.Get(name)
	if !ok {
		value = def
	}
	var n int64
	switch v := value.(type) {
	case int64:
		n = v
	case *big.Int:
		if v.Sign() < 0 {
			n = math.MinInt64
		} else {
			n = math.MaxInt64
		}
	case float64:
		switch {
		case math.IsNaN(v):
			return 0, &Raised{"ValueError"}
		case math.IsInf(v, 0):
			return 0, &Raised{"OverflowError"}
		case v >= math.MaxInt64:
			n = math.MaxInt64
		case v <= math.MinInt64:
			n = math.MinInt64
		default:
			n = int64(v)
		}
	default:
		return 0, Error(name + " must be a number")
	}
	return max(minimum, min(n, maximum)), nil
}

func TextArg(args py.Dict, name string, required bool) (text string, ok bool, err error) {
	value, _ := args.Get(name)
	if value == nil || value == "" {
		if required {
			return "", false, Error(name + " is required")
		}
		return "", false, nil
	}
	s, isString := value.(string)
	if !isString {
		return "", false, Error(name + " must be a string")
	}
	s = py.Strip(s)
	return s, s != "", nil
}

func Clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return py.Clip(text, limit-1) + "…"
}
