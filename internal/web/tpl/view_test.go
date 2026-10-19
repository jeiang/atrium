package tpl

import (
	"html/template"
	"reflect"
	"testing"
	"time"
)

type viewInner struct {
	ID    int    `json:"id"`
	Label string `json:"label,omitempty"`
}

type viewOuter struct {
	Title    string         `json:"title"`
	Inner    *viewInner     `json:"inner"`
	Missing  *viewInner     `json:"missing"`
	Rows     []viewInner    `json:"rows"`
	Nothing  []viewInner    `json:"nothing"`
	ByName   map[string]int `json:"by_name"`
	HTML     template.HTML  `json:"html"`
	When     time.Time      `json:"when"`
	Hidden   string         `json:"-"`
	Untagged int            `json:"-"`
	Plain    string
	private  string
	Any      any `json:"any"`
}

func TestViewMapsStructsToJSONKeys(t *testing.T) {
	when := time.Unix(1, 0)
	got := View(&viewOuter{
		Title: "t", Inner: &viewInner{ID: 1, Label: "l"}, Rows: []viewInner{{ID: 2}}, ByName: map[string]int{"a": 1},
		HTML: "<b>", When: when, Plain: "p", Any: viewInner{ID: 3},
	})
	want := map[string]any{
		"title":   "t",
		"inner":   map[string]any{"id": 1, "label": "l"},
		"missing": nil,
		"rows":    []any{map[string]any{"id": 2, "label": ""}},
		"nothing": []viewInner(nil),
		"by_name": map[string]any{"a": 1},
		"html":    template.HTML("<b>"),
		"when":    when,
		"Plain":   "p",
		"any":     map[string]any{"id": 3, "label": ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("View =\n%#v\nwant\n%#v", got, want)
	}
	if View(nil) != nil {
		t.Error("View(nil) != nil")
	}
}
