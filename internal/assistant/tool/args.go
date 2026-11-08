package tool

import (
	"encoding/json"
	"math"
	"math/big"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
)

func BoundedInt(args Args, name string, def, maximum, minimum int64) (int64, error) {
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
	case json.Number:
		if i, err := v.Int64(); err == nil {
			n = i
		} else {
			f, err := v.Float64()
			if err != nil {
				return 0, Error(name + " must be a number")
			}
			if n, err = clampFloat(f); err != nil {
				return 0, err
			}
		}
	case float64:
		var err error
		if n, err = clampFloat(v); err != nil {
			return 0, err
		}
	default:
		return 0, Error(name + " must be a number")
	}
	return max(minimum, min(n, maximum)), nil
}

func clampFloat(v float64) (int64, error) {
	switch {
	case math.IsNaN(v):
		return 0, Error("value is not a number")
	case math.IsInf(v, 0):
		return 0, Error("value is out of range")
	case v >= math.MaxInt64:
		return math.MaxInt64, nil
	case v <= math.MinInt64:
		return math.MinInt64, nil
	default:
		return int64(v), nil
	}
}

func IntArg(args Args, name string, required bool) (int64, bool, error) {
	value, ok := args.Get(name)
	if !ok || value == nil {
		if required {
			return 0, false, Error(name + " is required")
		}
		return 0, false, nil
	}
	switch v := value.(type) {
	case int64:
		return v, true, nil
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false, Error(name + " must be an integer")
		}
		return i, true, nil
	case float64:
		if v != math.Trunc(v) {
			return 0, false, Error(name + " must be an integer")
		}
		return int64(v), true, nil
	case *big.Int:
		if !v.IsInt64() {
			return 0, false, Error("value is too large")
		}
		return v.Int64(), true, nil
	}
	return 0, false, Error(name + " must be an integer")
}

func TextArg(args Args, name string, required bool) (text string, ok bool, err error) {
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
