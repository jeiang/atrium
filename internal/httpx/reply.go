package httpx

import (
	"fmt"
	"math"
)

// Key is Python's v[k] on a decoded JSON reply.
func Key(v any, k string) (any, error) {
	if m, ok := v.(map[string]any); ok {
		found, ok := m[k]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrKey, pyRepr(k))
		}
		return found, nil
	}
	return nil, subscriptError(v, "str")
}

// Index is Python's v[i] on a decoded JSON reply.
func Index(v any, i int) (any, error) {
	switch t := v.(type) {
	case []any:
		if i < 0 || i >= len(t) {
			return nil, fmt.Errorf("%w: list index out of range", ErrIndex)
		}
		return t[i], nil
	case map[string]any:
		return nil, fmt.Errorf("%w: %d", ErrKey, i)
	case string:
		r := []rune(t)
		if i < 0 || i >= len(r) {
			return nil, fmt.Errorf("%w: string index out of range", ErrIndex)
		}
		return string(r[i]), nil
	}
	return nil, subscriptError(v, "int")
}

func subscriptError(v any, index string) error {
	switch v.(type) {
	case []any:
		return fmt.Errorf("%w: list indices must be integers or slices, not %s", ErrType, index)
	case string:
		return fmt.Errorf("%w: string indices must be integers, not '%s'", ErrType, index)
	}
	return fmt.Errorf("%w: '%s' object is not subscriptable", ErrType, TypeName(v))
}

// TypeName is the Python type name of a decoded JSON value.
func TypeName(v any) string {
	switch t := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return "int"
		}
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// Truthy is Python's bool() of a decoded JSON value.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// Float is Python's float() of a decoded JSON number.
func Float(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("%w: unsupported operand type(s) for *: '%s' and '%s'", ErrType, TypeName(v), TypeName(v))
}
