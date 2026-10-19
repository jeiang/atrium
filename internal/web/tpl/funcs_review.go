package tpl

import (
	"html/template"
	"reflect"

	"github.com/jeiang/atrium/internal/py"
)

func init() {
	registerFuncs(map[string]any{
		"pystr":  pyScalar,
		"pyattr": pyAttr,
		"pylen":  pyLen,
	})
}

// pyScalar is Python's str() of a printed value: None, True and False, a float in repr form, an integer or a
// string as it is; a list or a mapping prints as its repr.
func pyScalar(v any) (string, error) {
	switch v.(type) {
	case nil, bool, float32, float64, string, template.HTML:
		return pyStr(v)
	}
	if s, err := pyStr(v); err == nil {
		return s, nil
	}
	return py.ReprValue(v), nil
}

// pyAttr is a Jinja attribute read printed: an absent key is Undefined and prints nothing, a key holding null
// prints None.
func pyAttr(m any, key string) (string, error) {
	var v any
	var found bool
	switch x := m.(type) {
	case map[string]any:
		v, found = x[key]
	case py.Dict:
		v, found = x.Get(key)
	}
	if !found {
		return "", nil
	}
	return pyScalar(v)
}

// pyLen is Jinja's length filter: a missing or null value has length 0.
func pyLen(v any) int {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case py.Dict:
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
