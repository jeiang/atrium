package tpl

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

func render(t *testing.T, text string, data any) string {
	t.Helper()
	e, err := New(Config{Extra: fstest.MapFS{"_t.gohtml": {Data: []byte(`{{define "_x"}}` + text + `{{end}}`)}}, Globals: map[string]any{"g": 7}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := e.Fragment(&out, "_x", &Frame{Data: data}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestFilters(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		data       any
		want       string
	}{
		{"ts date", `{{ts .Data.e "date"}}`, map[string]any{"e": int64(1760000000)}, "2025-10-09"},
		{"ts datetime", `{{ts .Data.e}}`, map[string]any{"e": 1760000000.0}, "2025-10-09 08:53"},
		{"ts iso", `{{ts .Data.e "iso"}}`, map[string]any{"e": int64(1760000000)}, "2025-10-09T08:53:20&#43;00:00"},
		{"ts iso micro", `{{ts .Data.e "iso"}}`, map[string]any{"e": 1760000000.5}, "2025-10-09T08:53:20.500000&#43;00:00"},
		{"ts epoch zero", `{{ts .Data.e "date"}}`, map[string]any{"e": int64(0)}, "1970-01-01"},
		{"ts none", `[{{ts .Data.e}}]`, map[string]any{"e": nil}, "[]"},
		{"ts empty", `[{{ts .Data.e}}]`, map[string]any{"e": ""}, "[]"},
		{"truncate", `{{truncate 12 .Data.s}}`, map[string]any{"s": "the quick brown fox jumps"}, "the..."},
		{"filesizeformat", `{{filesizeformat .Data.n}}`, map[string]any{"n": int64(1500)}, "1.5 kB"},
		{"urlencode", `{{urlencode .Data.s}}`, map[string]any{"s": "a b/é"}, "a%20b/%C3%A9"},
		{"round half even", `{{round .Data.v}} {{round .Data.w 1}}`, map[string]any{"v": 2.5, "w": 0.25}, "2 0.2"},
		{"int", `{{int .Data.v}}`, map[string]any{"v": 3.9}, "3"},
		{"format", `{{printf "%.0f%%" (mul .Data.v 100)}}`, map[string]any{"v": 0.8765}, "88%"},
		{"fdiv", `{{printf "%.1f" (fdiv .Data.v 1000)}}`, map[string]any{"v": int64(1500)}, "1.5"},
		{"join list", `{{join ", " .Data.l}}`, map[string]any{"l": []any{"a", int64(2)}}, "a, 2"},
		{"join scalar", `{{join ", " .Data.l}}`, map[string]any{"l": int64(7)}, "7"},
		{"capitalize", `{{capitalize .Data.s}}`, map[string]any{"s": "hELLO"}, "Hello"},
		{"replace", `{{replace "_" " " .Data.s}}`, map[string]any{"s": "a_b_c"}, "a b c"},
		{"selattr", `{{len (selattr "p" .Data.l)}}{{len (rejattr "p" .Data.l)}}`, map[string]any{"l": []any{map[string]any{"p": 1}, map[string]any{"p": 0}, map[string]any{"p": 1}}}, "21"},
		{"mapattr sumlens", `{{join "," (mapattr "n" .Data.l)}} {{sumlens "m" .Data.l}}`, map[string]any{"l": []any{map[string]any{"n": "a", "m": []any{1, 2}}, map[string]any{"n": "b", "m": []any{3}}}}, "a,b 3"},
		{"global", `{{global "g"}}`, nil, "7"},
		{"opt", `{{opt .Data "a" "d"}}{{opt .Data "b" "d"}}`, map[string]any{"a": "x"}, "xd"},
		{"labels", `{{lbl "stage" "knn"}} {{lbl "stage" "zzz"}} {{lbl "stage_dot" "zzz" "bg-chart-6"}}`, nil, "kNN zzz bg-chart-6"},
		{"safeurl", `{{safeurl "javascript:x"}}|{{safeurl "//e.com"}}|{{safeurl "/a b"}}|{{safeurl "https://e.com/x"}}|{{safeurl "mailto:a@b.c"}}|{{safeurl "JAVASCRIPT:x"}}`, nil, "#|#|/a b|https://e.com/x|mailto:a@b.c|#"},
		{"pct place", `{{pct .Data.p}} {{place .Data.n}} {{place "INBOX"}} {{place "A|B"}}`, map[string]any{"p": 0.5, "n": nil}, "50% Gone Inbox A · B"},
		{"proposal", `{{proposal "delete" nil}}/{{proposal "none" "x"}}/{{proposal "file" ""}}/{{proposal "file" "Dest"}}`, nil, "Delete/Leave in inbox/Leave in inbox/Dest"},
		{"sender", `{{sender .Data.a}}|{{sender .Data.b}}|{{sender .Data.c}}`, map[string]any{"a": map[string]any{"from_name": "N", "from_addr": "a"}, "b": map[string]any{"from_name": "", "from_addr": "a"}, "c": map[string]any{"from_name": nil}}, "N|a|Unknown sender"},
		{"page_path", `{{page_path .Data.p}}`, map[string]any{"p": []any{map[string]any{"title": "A"}, map[string]any{"title": "B"}}}, "A / B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.text, tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFilterErrors(t *testing.T) {
	e, err := New(Config{Extra: fstest.MapFS{"_t.gohtml": {Data: []byte(`{{define "_x"}}{{global "nope"}}{{end}}{{define "_y"}}{{ts "x"}}{{end}}{{define "_z"}}{{dict "a"}}{{end}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"_x", "_y", "_z"} {
		if err := e.Fragment(&bytes.Buffer{}, name, &Frame{}); err == nil {
			t.Errorf("%s did not fail", name)
		}
	}
}

func TestToastActionJavaScriptBoundary(t *testing.T) {
	e, err := New(Config{Extra: fstest.MapFS{"_t.gohtml": {Data: []byte(`{{define "_x"}}{{template "toast" .Data}}{{end}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	for onclick, want := range map[any]string{
		template.JS("doThing(1)"): `onclick="doThing(1)"`,
		"doThing(1)":              `onclick="&#34;doThing(1)&#34;"`,
	} {
		var out bytes.Buffer
		data := map[string]any{"title": "t", "action": map[string]any{"label": "l", "onclick": onclick}}
		if err := e.Fragment(&out, "_x", &Frame{Data: data}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "ZgotmplZ") {
			t.Errorf("onclick %#v rendered %s", onclick, out.String())
		}
	}
}
