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
