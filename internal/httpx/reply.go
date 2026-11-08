package httpx

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func Key(v any, k string) (any, error) {
	if m, ok := v.(map[string]any); ok {
		found, ok := m[k]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissing, strconv.Quote(k))
		}
		return found, nil
	}
	return nil, subscriptError(v, "str")
}

func Index(v any, i int) (any, error) {
	switch t := v.(type) {
	case []any:
		if i < 0 || i >= len(t) {
			return nil, fmt.Errorf("%w: index %d out of range", ErrRange, i)
		}
		return t[i], nil
	case map[string]any:
		return nil, fmt.Errorf("%w: %d", ErrMissing, i)
	case string:
		r := []rune(t)
		if i < 0 || i >= len(r) {
			return nil, fmt.Errorf("%w: index %d out of range", ErrRange, i)
		}
		return string(r[i]), nil
	}
	return nil, subscriptError(v, "int")
}

func subscriptError(v any, index string) error {
	switch v.(type) {
	case []any:
		return fmt.Errorf("%w: cannot index a list with %s", ErrShape, index)
	case string:
		return fmt.Errorf("%w: cannot index a string with %s", ErrShape, index)
	}
	return fmt.Errorf("%w: %s is not indexable", ErrShape, TypeName(v))
}

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

func Message(err error) string {
	text := err.Error()
	for _, class := range []error{ErrPayload, ErrMissing, ErrRange, ErrShape} {
		if errors.Is(err, class) {
			return strings.TrimPrefix(text, class.Error()+": ")
		}
	}
	return text
}
