package py

import (
	"encoding/json"
	"testing"
)

func TestJSONDumps(t *testing.T) {
	value := map[string]any{"a": "é\u007f\u2028😀", "b": []any{int64(1), 2.0}, "c": map[string]any{"z": 1, "y": nil}}
	got, err := JSONDumps(value, JSONOpts{EnsureASCII: true, SortKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a": "\u00e9\u007f\u2028\ud83d\ude00", "b": [1, 2.0], "c": {"y": null, "z": 1}}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	got, err = JSONDumps(map[string]any{"a": "é\u007f\u2028😀"}, JSONOpts{Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":\"é\u007f\u2028😀\"}"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestJSONRedump(t *testing.T) {
	for _, c := range []struct{ in, compact, spaced string }{
		{`{ "z" : "\u00e9", "a" : [ 1 , 1.0, 1e2, -0, 12345678901234567890 ] }`, `{"z":"é","a":[1,1.0,100.0,0,12345678901234567890]}`, `{"z": "é", "a": [1, 1.0, 100.0, 0, 12345678901234567890]}`},
		{`{"a":1,"a":2,"b":null,"c":true}`, `{"a":2,"b":null,"c":true}`, `{"a": 2, "b": null, "c": true}`},
		{`"</script>\u2028\"\/"`, "\"</script>\u2028\\\"/\"", "\"</script>\u2028\\\"/\""},
		{`[]`, `[]`, `[]`},
		{`{}`, `{}`, `{}`},
	} {
		got, err := JSONRedump([]byte(c.in), JSONOpts{Compact: true})
		if err != nil || got != c.compact {
			t.Errorf("compact %s = %s, %v; want %s", c.in, got, err, c.compact)
		}
		got, err = JSONRedump([]byte(c.in), JSONOpts{})
		if err != nil || got != c.spaced {
			t.Errorf("spaced %s = %s, %v; want %s", c.in, got, err, c.spaced)
		}
	}
	for _, in := range []string{``, `{`, `[1,]`, `1 2`, `{"a":}`} {
		if _, err := JSONRedump([]byte(in), JSONOpts{}); err == nil {
			t.Errorf("JSONRedump(%q) accepted", in)
		}
	}
}

func TestJSONObjectKeepsOrder(t *testing.T) {
	obj := Object{{"text", "x"}, {"done", false}, {"n", json.Number("3")}}
	got, err := JSONDumps(obj, JSONOpts{})
	if err != nil || got != `{"text": "x", "done": false, "n": 3}` {
		t.Errorf("got %s, %v", got, err)
	}
	got, err = JSONDumps(obj, JSONOpts{SortKeys: true})
	if err != nil || got != `{"done": false, "n": 3, "text": "x"}` {
		t.Errorf("sorted: got %s, %v", got, err)
	}
	if _, err := JSONDumps(struct{}{}, JSONOpts{}); err == nil {
		t.Error("a struct dumped")
	}
}
