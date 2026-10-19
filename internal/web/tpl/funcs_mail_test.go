package tpl

import (
	"testing"

	"github.com/jeiang/atrium/internal/rx"
)

func TestMailFuncs(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		data       any
		want       string
	}{
		{"percent float", `{{percent .Data.v}}`, map[string]any{"v": 70.0}, "70%"},
		{"percent int64", `{{percent .Data.v}}`, map[string]any{"v": int64(70)}, "70%"},
		{"percent half even", `{{percent .Data.v}} {{percent .Data.w}}`, map[string]any{"v": 0.5, "w": 1.5}, "0% 2%"},
		{"splitin", `{{splitin "|" .Data.l "b"}}{{splitin "|" .Data.l "x"}}{{splitin "|" nil "x"}}{{splitin "|" "" ""}}`, map[string]any{"l": "a|b|c"}, "truefalsefalsetrue"},
		{"sumvalues", `{{sumvalues .Data.m}}`, map[string]any{"m": map[string]any{"a": int64(1), "b": 2, "c": 0.0}}, "3"},
		{"sumvalues typed map", `{{sumvalues .Data.m}}`, map[string]any{"m": map[string]int{"a": 4, "b": 5}}, "9"},
		{"queryval keeps case and slashes", `<a href="/x?a={{queryval .Data.v}}">`, map[string]any{"v": "a b/<é&"}, `<a href="/x?a=a%20b/%3C%C3%A9%26">`},
		{"queryval none", `<a href="/x?a={{queryval .Data.v}}">`, map[string]any{"v": "icloud"}, `<a href="/x?a=icloud">`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.text, tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	pattern := rx.MustCompile(`^cond-[1-9][0-9]{5}$`, 0)
	seen := map[string]bool{}
	for range 50 {
		id := render(t, `{{condid}}`, nil)
		if m, _ := pattern.Search(id); m == nil {
			t.Fatalf("condid = %q", id)
		}
		seen[id] = true
	}
	if len(seen) < 40 {
		t.Errorf("condid repeats too often: %d distinct of 50", len(seen))
	}
}
