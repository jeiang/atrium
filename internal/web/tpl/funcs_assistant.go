package tpl

import "reflect"

func init() {
	registerFuncs(map[string]any{"has": has})
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
