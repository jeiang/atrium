package api

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/web"
)

var (
	limitType     = reflect.TypeFor[Limit]()
	cursorType    = reflect.TypeFor[Cursor]()
	timeType      = reflect.TypeFor[time.Time]()
	unmarshalType = reflect.TypeFor[json.Unmarshaler]()
)

type rules struct {
	min, max *int64
	enum     []string
}

func parseRules(tag string) rules {
	var out rules
	for part := range strings.SplitSeq(tag, ",") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "min", "max":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				panic("api: bad " + key + " rule " + tag)
			}
			if key == "min" {
				out.min = &n
			} else {
				out.max = &n
			}
		case "enum":
			out.enum = strings.Split(value, "|")
		}
	}
	return out
}

func decode(w http.ResponseWriter, r *http.Request, op application.Name, v any, body application.BodyKind) bool {
	rv := reflect.ValueOf(v).Elem()
	query := r.URL.Query()
	scope := maps.Clone(query)
	if scope == nil {
		scope = url.Values{}
	}
	for _, f := range reflect.VisibleFields(rv.Type()) {
		if name, ok := f.Tag.Lookup("path"); ok {
			scope.Set("{"+name+"}", web.PathParam(r, name))
		}
	}
	for _, f := range reflect.VisibleFields(rv.Type()) {
		if f.Anonymous || !f.IsExported() {
			continue
		}
		field := rv.FieldByIndex(f.Index)
		if name, ok := f.Tag.Lookup("path"); ok {
			if setValue(field, web.PathParam(r, name), parseRules(f.Tag.Get("api"))) != "" {
				web.APIError(w, http.StatusNotFound, "not_found", "Not found.")
				return false
			}
			continue
		}
		if name, ok := f.Tag.Lookup("query"); ok {
			raw := web.QueryValue(query, name)
			switch f.Type {
			case limitType:
				n, err := strconv.Atoi(raw)
				if raw == "" {
					n, err = LimitDefault, nil
				}
				if err != nil || n < 1 || n > LimitMax {
					web.APIError(w, http.StatusUnprocessableEntity, "invalid", "limit must be between 1 and 100.",
						web.FieldError{Type: "int_range", Loc: []string{"query", name}, Msg: "Input should be between 1 and 100", Input: raw})
					return false
				}
				field.SetInt(int64(n))
			case cursorType:
				c, ok := parseCursor(op, scope, raw)
				if !ok {
					web.APIError(w, http.StatusBadRequest, "bad_cursor", "The cursor is not valid.")
					return false
				}
				field.Set(reflect.ValueOf(c))
			default:
				if raw == "" {
					continue
				}
				if msg := setValue(field, raw, parseRules(f.Tag.Get("api"))); msg != "" {
					web.APIError(w, http.StatusUnprocessableEntity, "invalid", "The "+name+" parameter is not valid.",
						web.FieldError{Type: "value_error", Loc: []string{"query", name}, Msg: msg, Input: raw})
					return false
				}
			}
			continue
		}
		if name, ok := f.Tag.Lookup("header"); ok {
			raw := r.Header.Get(name)
			if raw == "" {
				continue
			}
			if msg := setValue(field, raw, parseRules(f.Tag.Get("api"))); msg != "" {
				web.APIError(w, http.StatusUnprocessableEntity, "invalid", "The "+name+" header is not valid.",
					web.FieldError{Type: "value_error", Loc: []string{"header", name}, Msg: msg, Input: raw})
				return false
			}
		}
	}
	switch body {
	case application.BodyJSON:
		return decodeBody(w, r, v, rv.Type())
	case application.BodyNone:
		if r.ContentLength != 0 {
			web.APIError(w, http.StatusBadRequest, "bad_request", "This request takes no body.")
			return false
		}
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, t reflect.Type) bool {
	if len(checkBody(t, json.RawMessage("{}"), nil)) == 0 {
		return web.DecodeJSON(w, r, v)
	}
	var raw bytes.Buffer
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(r.Body, &raw), r.Body}
	if !web.DecodeOptionalJSON(w, r, v) {
		return false
	}
	body := bytes.TrimSpace(raw.Bytes())
	if len(body) == 0 {
		body = []byte("{}")
	}
	if errs := checkBody(t, body, []string{"body"}); len(errs) > 0 {
		message := "A required field is missing."
		if errs[0].Type != "missing" {
			message = "A field is not valid."
		}
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", message, errs...)
		return false
	}
	return true
}

func checkBody(t reflect.Type, raw json.RawMessage, loc []string) []web.FieldError {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType || reflect.PointerTo(t).Implements(unmarshalType) {
		return nil
	}
	var out []web.FieldError
	switch t.Kind() {
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for i, item := range items {
			out = append(out, checkBody(t.Elem(), item, append(slices.Clip(loc), strconv.Itoa(i)))...)
		}
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil
		}
		for _, f := range reflect.VisibleFields(t) {
			tag := f.Tag.Get("json")
			if f.Anonymous || !f.IsExported() || tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			at := append(slices.Clip(loc), name)
			value, ok := fields[name]
			if !ok || string(value) == "null" {
				if f.Type.Kind() != reflect.Pointer && !strings.Contains(","+opts+",", ",omitempty,") && !strings.Contains(","+opts+",", ",omitzero,") {
					out = append(out, web.FieldError{Type: "missing", Loc: at, Msg: "Field required"})
				}
				continue
			}
			if enum := parseRules(f.Tag.Get("api")).enum; len(enum) > 0 {
				var s string
				if json.Unmarshal(value, &s) == nil && !slices.Contains(enum, s) {
					out = append(out, web.FieldError{Type: "enum", Loc: at, Msg: "Input should be one of " + strings.Join(enum, ", "), Input: s})
				}
			}
			out = append(out, checkBody(f.Type, value, at)...)
		}
	}
	return out
}

func setValue(field reflect.Value, raw string, rule rules) string {
	if field.Kind() == reflect.Pointer {
		next := reflect.New(field.Type().Elem())
		if msg := setValue(next.Elem(), raw, rule); msg != "" {
			return msg
		}
		field.Set(next)
		return ""
	}
	if field.Type() == timeType {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "Input should be an RFC 3339 date-time"
		}
		field.Set(reflect.ValueOf(t))
		return ""
	}
	switch field.Kind() {
	case reflect.String:
		if len(rule.enum) > 0 && !slices.Contains(rule.enum, raw) {
			return "Input should be one of " + strings.Join(rule.enum, ", ")
		}
		field.SetString(raw)
	case reflect.Int, reflect.Int64, reflect.Int32:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || field.OverflowInt(n) {
			return "Input should be an integer"
		}
		if (rule.min != nil && n < *rule.min) || (rule.max != nil && n > *rule.max) {
			return "Input is out of range"
		}
		field.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return "Input should be a boolean"
		}
		field.SetBool(b)
	default:
		panic("api: unsupported parameter type " + field.Type().String())
	}
	return ""
}
