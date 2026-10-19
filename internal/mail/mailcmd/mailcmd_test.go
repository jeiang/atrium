package mailcmd

import (
	"testing"

	"github.com/jeiang/atrium/internal/py"
)

func TestSelectionDocument(t *testing.T) {
	object, err := py.JSONLoads([]byte(`{"groups": [{"name": "A", "message_ids": [1, 2]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := selectionDocument(object)
	if err != nil || len(got) != 1 || got[0].Key != "groups" {
		t.Errorf("selectionDocument(object) = %v, %v", got, err)
	}
	for _, text := range []string{`null`, `{}`, `[]`, `0`, `""`, `false`} {
		parsed, _ := py.JSONLoads([]byte(text))
		if got, err := selectionDocument(parsed); got != nil || err != nil {
			t.Errorf("selectionDocument(%s) = %v, %v; want no selection", text, got, err)
		}
	}
	for text, want := range map[string]string{`[1]`: "list", `3`: "int", `"x"`: "str", `true`: "bool", `1.5`: "float"} {
		parsed, _ := py.JSONLoads([]byte(text))
		_, err := selectionDocument(parsed)
		if err == nil || err.Error() != "'"+want+"' object has no attribute 'get'" {
			t.Errorf("selectionDocument(%s) error = %v", text, err)
		}
	}
}
