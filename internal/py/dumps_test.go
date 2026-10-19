package py

import (
	"encoding/json"
	"testing"
)

func TestDumps(t *testing.T) {
	value := map[string]any{"a": "é\u007f\u2028😀", "b": []any{int64(1), 2.0}, "c": map[string]any{"z": 1, "y": nil}}
	got, err := Dumps(value, JSONOpts{EnsureASCII: true, SortKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a": "\u00e9\u007f\u2028\ud83d\ude00", "b": [1, 2.0], "c": {"y": null, "z": 1}}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	got, err = Dumps(map[string]any{"a": "é\u007f\u2028😀"}, JSONOpts{Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":\"é\u007f\u2028😀\"}"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestRedump(t *testing.T) {
	for _, c := range []struct{ in, compact, spaced string }{
		{`{ "z" : "\u00e9", "a" : [ 1 , 1.0, 1e2, -0, 12345678901234567890 ] }`, `{"z":"é","a":[1,1.0,100.0,0,12345678901234567890]}`, `{"z": "é", "a": [1, 1.0, 100.0, 0, 12345678901234567890]}`},
		{`{"a":1,"a":2,"b":null,"c":true}`, `{"a":2,"b":null,"c":true}`, `{"a": 2, "b": null, "c": true}`},
		{`"</script>\u2028\"\/"`, "\"</script>\u2028\\\"/\"", "\"</script>\u2028\\\"/\""},
		{`[]`, `[]`, `[]`},
		{`{}`, `{}`, `{}`},
	} {
		got, err := Redump([]byte(c.in), JSONOpts{Compact: true})
		if err != nil || got != c.compact {
			t.Errorf("compact %s = %s, %v; want %s", c.in, got, err, c.compact)
		}
		got, err = Redump([]byte(c.in), JSONOpts{})
		if err != nil || got != c.spaced {
			t.Errorf("spaced %s = %s, %v; want %s", c.in, got, err, c.spaced)
		}
	}
	for _, in := range []string{``, `{`, `[1,]`, `1 2`, `{"a":}`} {
		if _, err := Redump([]byte(in), JSONOpts{}); err == nil {
			t.Errorf("Redump(%q) accepted", in)
		}
	}
}

func TestDictKeepsOrder(t *testing.T) {
	obj := Dict{{"text", "x"}, {"done", false}, {"n", json.Number("3")}}
	got, err := Dumps(obj, JSONOpts{})
	if err != nil || got != `{"text": "x", "done": false, "n": 3}` {
		t.Errorf("got %s, %v", got, err)
	}
	got, err = Dumps(obj, JSONOpts{SortKeys: true})
	if err != nil || got != `{"done": false, "n": 3, "text": "x"}` {
		t.Errorf("sorted: got %s, %v", got, err)
	}
	if _, err := Dumps(struct{}{}, JSONOpts{}); err == nil {
		t.Error("a struct dumped")
	}
}
