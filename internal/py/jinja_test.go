package py

import "testing"

func TestJSONHTMLSafeNestedValues(t *testing.T) {
	in := map[string]any{
		"b": int64(1),
		"a": []any{1.5, 1e16, 1e-5, nil, true, "é\u2028 \U0001F389\x7f"},
		"z": map[string]any{},
	}
	got, err := JSONHTMLSafe(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": [1.5, 1e+16, 1e-05, null, true, "\u00e9\u2028 \ud83c\udf89\u007f"], "b": 1, "z": {}}`
	if got != want {
		t.Errorf("JSONHTMLSafe = %s, want %s", got, want)
	}
}

func TestJSONHTMLSafe(t *testing.T) {
	got, err := JSONHTMLSafe(map[string]any{"X-CSRF-Token": `a<b>&'c"`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"X-CSRF-Token": "a\u003cb\u003e\u0026\u0027c\""}`; got != want {
		t.Errorf("JSONHTMLSafe = %s, want %s", got, want)
	}
	if _, err := JSONHTMLSafe(make(chan int)); err == nil {
		t.Error("JSONHTMLSafe accepted a channel")
	}
}

func TestRound(t *testing.T) {
	for _, tc := range []struct {
		in     float64
		digits int
		want   float64
	}{
		{2.5, 0, 2}, {3.5, 0, 4}, {-0.5, 0, 0}, {0.125, 2, 0.12}, {2.675, 2, 2.67}, {1234.5678, 1, 1234.6},
	} {
		if got := Round(tc.in, tc.digits); got != tc.want {
			t.Errorf("Round(%v, %d) = %v, want %v", tc.in, tc.digits, got, tc.want)
		}
	}
}

func TestURLQuote(t *testing.T) {
	if got, want := URLQuote("a b/c?d=é&~_.-+ *'()!"), "a%20b/c%3Fd%3D%C3%A9%26~_.-%2B%20%2A%27%28%29%21"; got != want {
		t.Errorf("URLQuote = %s, want %s", got, want)
	}
}

func TestFileSizeFormat(t *testing.T) {
	for in, want := range map[float64]string{
		0: "0 Bytes", 1: "1 Byte", 999: "999 Bytes", 1000: "1.0 kB", 1500: "1.5 kB", 999999: "1000.0 kB", 1e6: "1.0 MB", 2.5e9: "2.5 GB",
	} {
		if got := FileSizeFormat(in); got != want {
			t.Errorf("FileSizeFormat(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdefghij", 5, "abcdefghij"},
		{"the quick brown fox jumps", 12, "the..."}, {"the quick brown fox jumps", 16, "the quick..."},
		{"abcdefghijklmnopqrstuvwxyz", 10, "abcdefg..."},
		{"東京東京東京東京東京東京", 5, "東京..."},
	} {
		if got := Truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
