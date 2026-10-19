package web

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// PathParam is the path parameter name of the matched route, percent-decoded: chi routes on the escaped path when
// the request has one that differs from the default encoding, and then leaves the parameter escaped.
func PathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if r.URL.RawPath != "" {
		if decoded, err := url.PathUnescape(v); err == nil {
			return decoded
		}
	}
	return v
}

// FormField is the value FastAPI binds to a scalar Form() parameter: the last value sent, where an empty string
// counts as not sent (ok is false), so the parameter takes its default or is missing.
func FormField(form url.Values, name string) (value string, ok bool) {
	values := form[name]
	if len(values) == 0 || values[len(values)-1] == "" {
		return "", false
	}
	return values[len(values)-1], true
}

// FormString is FormField with a default for a parameter that has one.
func FormString(form url.Values, name, def string) string {
	if value, ok := FormField(form, name); ok {
		return value
	}
	return def
}

// FieldError is one entry of FastAPI's request validation response.
type FieldError struct {
	Type  string         `json:"type"`
	Loc   []string       `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

// BadInt is the error of an integer parameter that is not one; where is "query" or "path".
func BadInt(where, name, input string) FieldError {
	return FieldError{
		Type: "int_parsing", Loc: []string{where, name}, Input: input,
		Msg: "Input should be a valid integer, unable to parse string as an integer",
	}
}

// BadLiteral is the error of a parameter that is none of the quoted choices, such as "'row'" and "'message'".
func BadLiteral(where, name, input string, choices ...string) FieldError {
	expected := strings.Join(choices[:len(choices)-1], ", ") + " or " + choices[len(choices)-1]
	return FieldError{
		Type: "literal_error", Loc: []string{where, name}, Input: input,
		Msg: "Input should be " + expected, Ctx: map[string]any{"expected": expected},
	}
}

// Missing is the error of a required parameter that was not sent.
func Missing(where, name string) FieldError {
	return FieldError{Type: "missing", Loc: []string{where, name}, Msg: "Field required"}
}

// Unprocessable answers 422 with FastAPI's {"detail": [...]} body, which is what a request that fails
// validation gets before any handler runs.
func Unprocessable(w http.ResponseWriter, errs ...FieldError) {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string][]FieldError{"detail": errs})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write(bytes.TrimRight(body.Bytes(), "\n"))
}

// LastValue is the last of values, def when there are none: how FastAPI binds a scalar parameter.
func LastValue(values []string, def string) string {
	if len(values) == 0 {
		return def
	}
	return values[len(values)-1]
}

// QueryValue is the last value of a query parameter, "" when absent.
func QueryValue(q url.Values, name string) string { return LastValue(q[name], "") }

// FormValues is the body of a form post, urlencoded or multipart; an unreadable body is empty.
func FormValues(r *http.Request) url.Values {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(media, "multipart/") {
		if err := r.ParseMultipartForm(BodyCap); err != nil || r.MultipartForm == nil {
			return url.Values{}
		}
		return r.MultipartForm.Value
	}
	_ = r.ParseForm()
	return r.PostForm
}

// FormLast is a field as Starlette's form.get reads it: the last value sent, an empty string included, and
// ok false only when the field was not sent. Parameters declared with Form() use FormField instead.
func FormLast(form url.Values, name string) (value string, ok bool) {
	values := form[name]
	if len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}
