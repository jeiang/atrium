package tpl

import (
	"fmt"
	"html/template"
	"math/rand/v2"
	"reflect"
	"strings"
)

func init() {
	registerFuncs(map[string]any{
		"condid":    condID,
		"percent":   percent,
		"splitin":   splitIn,
		"queryval":  queryVal,
		"sumvalues": sumValues,
	})
}

func condID() string {
	return fmt.Sprintf("cond-%d", 100000+rand.IntN(900000))
}

func percent(v any) (string, error) {
	f, err := toFloat(v)
	return fmt.Sprintf("%.0f%%", f), err
}

func splitIn(sep string, s, item any) bool {
	want := str(item)
	for _, part := range strings.Split(str(s), sep) {
		if part == want {
			return true
		}
	}
	return false
}

func sumValues(m any) (int64, error) {
	var total int64
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map {
		return 0, fmt.Errorf("sumvalues: not a mapping: %T", m)
	}
	for _, key := range rv.MapKeys() {
		f, err := toFloat(rv.MapIndex(key).Interface())
		if err != nil {
			return 0, err
		}
		total += int64(f)
	}
	return total, nil
}

// queryVal is |urlencode of a value in the query of a URL attribute: html/template would percent-encode the "%" of
// an encoded string there, and a template.URL is only normalized.
func queryVal(v any) (template.URL, error) {
	s, err := urlencode(v)
	return template.URL(s), err
}
