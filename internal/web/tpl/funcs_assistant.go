package tpl

import (
	"fmt"
	"reflect"
)

func init() {
	registerFuncs(map[string]any{"has": has, "ctxlabel": ctxlabel})
}

func has(list, item any) bool {
	rv := reflect.ValueOf(list)
	if list == nil || rv.Kind() != reflect.Slice {
		return false
	}
	for i := range rv.Len() {
		if reflect.DeepEqual(rv.Index(i).Interface(), item) {
			return true
		}
	}
	return false
}

func ctxlabel(v any) (string, error) {
	f, err := toFloat(v)
	if err != nil {
		return "", err
	}
	if f >= 1_000_000 {
		return fmt.Sprintf("%dM", int64(f/1_000_000)), nil
	}
	return fmt.Sprintf("%dK", int64(f/1000)), nil
}
