package web

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
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

// ParseInt is how FastAPI reads an integer parameter: surrounding whitespace is ignored and an integral float
// text such as "5.0" is accepted.
func ParseInt(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && f == math.Trunc(f) && math.Abs(f) < 1<<63 {
		return int64(f), true
	}
	return 0, false
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
