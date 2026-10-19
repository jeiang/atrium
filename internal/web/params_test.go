package web

import (
	"net/http"
	"net/http/httptest"
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

func TestParseInt(t *testing.T) {
	for in, want := range map[string]struct {
		n  int64
		ok bool
	}{
		"5": {5, true}, " 7 ": {7, true}, "-3": {-3, true}, "+4": {4, true}, "5.0": {5, true}, "5.5": {0, false},
		"": {0, false}, "x": {0, false}, "1e2": {100, true}, "inf": {0, false}, "99999999999999999999": {0, false},
	} {
		if n, ok := ParseInt(in); n != want.n || ok != want.ok {
			t.Errorf("ParseInt(%q) = %d, %v", in, n, ok)
		}
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

func TestSplitURL(t *testing.T) {
	for in, want := range map[string][3]string{
		"http://testserver/triage?stage=llm#top": {"testserver", "/triage", "stage=llm"},
		"//host:8080/a/b?x":                      {"host:8080", "/a/b", "x"},
		"/just/a/path?q=1":                       {"", "/just/a/path", "q=1"},
		"":                                       {"", "", ""},
		"http://testserver":                      {"testserver", "", ""},
		"http://testserver?q":                    {"testserver", "", "q"},
		"  http://t\t/a\nb":                      {"t", "/ab", ""},
		"mailto:a@b":                             {"", "a@b", ""},
		"http://testserver//evil.test":           {"testserver", "//evil.test", ""},
		"1http://x/y":                            {"", "1http://x/y", ""},
	} {
		netloc, path, query, ok := SplitURL(in)
		if !ok || [3]string{netloc, path, query} != want {
			t.Errorf("SplitURL(%q) = %q %q %q %v", in, netloc, path, query, ok)
		}
	}
	if _, _, _, ok := SplitURL("http://[::1/x"); ok {
		t.Error("unbalanced brackets are an error")
	}
}
