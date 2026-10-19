package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestPathParamDecodesOnlyWhenChiRoutedOnTheEscapedPath(t *testing.T) {
	r := chi.NewRouter()
	var got []string
	r.Get("/rules/{id}/edit", func(_ http.ResponseWriter, r *http.Request) { got = append(got, PathParam(r, "id")) })
	for _, path := range []string{
		"/rules/plain/edit", "/rules/a%20b/edit", "/rules/a%26b/edit", "/rules/100%25/edit", "/rules/%C3%A9%3F%23/edit", "/rules/a%2526/edit",
	} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	want := []string{"plain", "a b", "a&b", "100%", "é?#", "a%26"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("PathParam = %q, want %q", got, want)
	}
}

func TestFormFieldTreatsAnEmptyValueAsNotSent(t *testing.T) {
	form := url.Values{"a": {"x", ""}, "b": {"", "y"}, "c": {""}, "d": {"  "}}
	for name, want := range map[string]struct {
		value string
		ok    bool
	}{"a": {"", false}, "b": {"y", true}, "c": {"", false}, "d": {"  ", true}, "missing": {"", false}} {
		if value, ok := FormField(form, name); value != want.value || ok != want.ok {
			t.Errorf("FormField(%q) = %q, %v", name, value, ok)
		}
	}
	if got := FormString(form, "c", "dflt"); got != "dflt" {
		t.Errorf("FormString = %q", got)
	}
	if got := FormString(form, "b", "dflt"); got != "y" {
		t.Errorf("FormString = %q", got)
	}
}

func TestUnprocessableIsFastAPIsValidationBody(t *testing.T) {
	rec := httptest.NewRecorder()
	Unprocessable(rec, BadInt("query", "offset", "x"), BadLiteral("body", "direction", "no", "'up'", "'down'"), Missing("body", "direction"))
	want := `{"detail":[{"type":"int_parsing","loc":["query","offset"],"msg":"Input should be a valid integer, unable to parse string as an integer","input":"x"},` +
		`{"type":"literal_error","loc":["body","direction"],"msg":"Input should be 'up' or 'down'","input":"no","ctx":{"expected":"'up' or 'down'"}},` +
		`{"type":"missing","loc":["body","direction"],"msg":"Field required","input":null}]}`
	if rec.Code != 422 || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != want {
		t.Errorf("%d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}
