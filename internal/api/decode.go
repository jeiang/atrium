package api

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/web"
)

var (
	limitType  = reflect.TypeFor[Limit]()
	cursorType = reflect.TypeFor[Cursor]()
	timeType   = reflect.TypeFor[time.Time]()
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
	cursorQuery := r.URL.Query()
	for _, f := range reflect.VisibleFields(rv.Type()) {
		if name, ok := f.Tag.Lookup("path"); ok {
			cursorQuery.Set("$path:"+name, web.PathParam(r, name))
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
				c, ok := parseCursor(op, cursorQuery, raw)
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
	case application.BodyMultipart:
		media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media == "application/json" {
			return web.DecodeJSON(w, r, v)
		}
		decoder, ok := v.(interface{ DecodeMultipart(*multipart.Reader) error })
		if err != nil || params["boundary"] == "" || !ok {
			web.APIError(w, http.StatusBadRequest, "bad_request", "Invalid multipart body.")
			return false
		}
		err = decoder.DecodeMultipart(multipart.NewReader(r.Body, params["boundary"]))
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			web.APIError(w, 413, "too_large", "Request too large.")
			return false
		}
		if err != nil {
			var appErr *application.Error
			if errors.As(err, &appErr) {
				web.APIFail(w, err)
			} else {
				if _, drainErr := io.Copy(io.Discard, r.Body); errors.As(drainErr, &large) {
					web.APIError(w, 413, "too_large", "Request too large.")
					return false
				}
				web.APIError(w, 400, "bad_request", "Invalid multipart body.")
			}
			return false
		}
		return true
	case application.BodyJSON:
		return web.DecodeJSON(w, r, v)
	case application.BodyNone:
		if r.ContentLength != 0 {
			web.APIError(w, http.StatusBadRequest, "bad_request", "This request takes no body.")
			return false
		}
	}
	return true
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
