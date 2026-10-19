package py

import "testing"

func TestJSONDumps(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{true, "true"},
		{int64(-3), "-3"},
		{[]string{}, "[]"},
		{[]string{`\Seen`, "a\"b", "é", "\U0001f600", "\x7f\n\x01"}, `["\\Seen", "a\"b", "\u00e9", "\ud83d\ude00", "\u007f\n\u0001"]`},
		{Dict{{"from", "INBOX"}, {"flags", []any{[]string{}, []string{`\Seen`}}}, {"in_inbox", []any{0, 1}}, {"to_id", int64(7)}},
			`{"from": "INBOX", "flags": [[], ["\\Seen"]], "in_inbox": [0, 1], "to_id": 7}`},
		{Dict{}, "{}"},
	} {
		if got := JSONDumps(tc.in); got != tc.want {
			t.Errorf("JSONDumps(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestJSONDumpsOptions(t *testing.T) {
	value := map[string]any{"a": "é\u007f\u2028😀", "b": []any{int64(1), 2.0}, "c": map[string]any{"z": 1, "y": nil}}
	if got, want := JSONDumps(value), `{"a": "\u00e9\u007f\u2028\ud83d\ude00", "b": [1, 2.0], "c": {"y": null, "z": 1}}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := JSONDumps(Dict{{"a", "é\u007f\u2028😀"}, {"b", []any{1, 2.5}}}, JSONOpts{NoEnsureASCII: true, Compact: true}), "{\"a\":\"é\u007f\u2028😀\",\"b\":[1,2.5]}"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	obj := Dict{{"text", "x"}, {"done", false}}
	if got := JSONDumps(obj, JSONOpts{SortKeys: true}); got != `{"done": false, "text": "x"}` {
		t.Errorf("sorted: %s", got)
	}
	if got := JSONDumps(obj); got != `{"text": "x", "done": false}` || obj[0].Key != "text" {
		t.Errorf("unsorted: %s", got)
	}
}

func TestJSONRedump(t *testing.T) {
	for _, c := range []struct{ in, compact, spaced string }{
		{`{ "z" : "\u00e9", "a" : [ 1 , 1.0, 1e2, -0, 12345678901234567890 ] }`, `{"z":"é","a":[1,1.0,100.0,0,12345678901234567890]}`, `{"z": "\u00e9", "a": [1, 1.0, 100.0, 0, 12345678901234567890]}`},
		{`{"a":1,"a":2,"b":null,"c":true}`, `{"a":2,"b":null,"c":true}`, `{"a": 2, "b": null, "c": true}`},
		{`[]`, `[]`, `[]`},
	} {
		got, err := JSONRedump([]byte(c.in), JSONOpts{NoEnsureASCII: true, Compact: true})
		if err != nil || got != c.compact {
			t.Errorf("compact %s = %s, %v; want %s", c.in, got, err, c.compact)
		}
		got, err = JSONRedump([]byte(c.in))
		if err != nil || got != c.spaced {
			t.Errorf("spaced %s = %s, %v; want %s", c.in, got, err, c.spaced)
		}
	}
	for _, in := range []string{``, `{`, `[1,]`, `1 2`} {
		if _, err := JSONRedump([]byte(in)); err == nil {
			t.Errorf("JSONRedump(%q) accepted", in)
		}
	}
}
func TestJSONDumpsIntegerSlices(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{[]int{}, "[]"}, {[]int{-1, 2}, "[-1, 2]"}, {[]int64{9007199254740993}, "[9007199254740993]"},
		{[]uint32{4294967295, 0}, "[4294967295, 0]"}, {Dict{{Key: "uids", Value: []uint32{1, 2}}}, `{"uids": [1, 2]}`},
	} {
		if got := JSONDumps(c.v); got != c.want {
			t.Errorf("JSONDumps(%#v) = %s, want %s", c.v, got, c.want)
		}
	}
}
