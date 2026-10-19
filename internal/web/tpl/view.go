package tpl

import (
	"reflect"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/py"
)

var timeType = reflect.TypeOf(time.Time{})

// View converts typed view structs to the map shape templates read: struct fields become keys named by their
// json tag, so a template reads .thread.created_at for both a struct and a decoded JSON document.
func View(v any) any {
	return view(reflect.ValueOf(v))
}

func view(rv reflect.Value) any {
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	if d, ok := rv.Interface().(py.Dict); ok {
		out := py.OrderedMap(d)
		for k, e := range out {
			if k != py.OrderKey {
				out[k] = view(reflect.ValueOf(e))
			}
		}
		return out
	}
	switch rv.Kind() {
	case reflect.Struct:
		if rv.Type() == timeType {
			return rv.Interface()
		}
		out := make(map[string]any, rv.NumField())
		for i := range rv.NumField() {
			field := rv.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name := field.Name
			if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag == "-" {
				continue
			} else if tag != "" {
				name = tag
			}
			out[name] = view(rv.Field(i))
		}
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv.Interface()
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = view(rv.Index(i))
		}
		return out
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return rv.Interface()
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			if key := iter.Key().String(); key == py.OrderKey {
				out[key] = iter.Value().Interface()
				continue
			}
			out[iter.Key().String()] = view(iter.Value())
		}
		return out
	}
	return rv.Interface()
}
