package tpl

import (
	"fmt"
	"html/template"
	"reflect"
)

func init() {
	registerFuncs(map[string]any{
		"pystr":  pyScalar,
		"pyattr": pyAttr,
		"pylen":  pyLen,
	})
}

func pyScalar(v any) (string, error) {
	switch v.(type) {
	case nil, bool, float32, float64, string, template.HTML:
		return pyStr(v)
	}
	if s, err := pyStr(v); err == nil {
		return s, nil
	}
	return fmt.Sprint(v), nil
}

func pyAttr(m any, key string) (string, error) {
	var v any
	var found bool
	switch x := m.(type) {
	case map[string]any:
		v, found = x[key]
	case []entry:
		for i := len(x) - 1; i >= 0; i-- {
			if x[i].Key == key {
				v, found = x[i].Value, true
				break
			}
		}
	}
	if !found {
		return "", nil
	}
	return pyScalar(v)
}

func pyLen(v any) int {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case []entry:
		return len(x)
	case string:
		return len([]rune(x))
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len()
	}
	return 0
}
