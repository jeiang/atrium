package py

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRepr(t *testing.T) {
	for in, want := range map[string]string{
		"":         "''",
		"dry":      "'dry'",
		"it's":     `"it's"`,
		`a'b"c`:    `'a\'b"c'`,
		`a"b'c`:    `'a"b\'c'`,
		"a\\b":     `'a\\b'`,
		"x\ny\tz":  `'x\ny\tz'`,
		"\x00":     `'\x00'`,
		"\x01":     `'\x01'`,
		"\u00a0":   `'\xa0'`,
		"é":        "'é'",
		"/v1/chat": "'/v1/chat'",
	} {
		if got := Repr(in); got != want {
			t.Errorf("Repr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStr(t *testing.T) {
	if got := Strip("\x1c\u00a0 a b\u3000\n"); got != "a b" {
		t.Errorf("Strip = %q", got)
	}
	if got := Split(" a\x1db\u2003c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Split = %q", got)
	}
	for in, want := range map[string][]string{
		"":              nil,
		"a\nb":          {"a", "b"},
		"a\r\nb\rc\n":   {"a", "b", "c"},
		"\n":            {""},
		"a\x1cb\u2028c": {"a", "b", "c"},
		"a\n\nb":        {"a", "", "b"},
	} {
		if got := SplitLines(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitLines(%q) = %q", in, got)
		}
	}
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{{"héllo", 2, "hé"}, {"abc", 0, ""}, {"abc", 5, "abc"}, {"日本語", 2, "日本"}} {
		if got := Clip(tc.in, tc.n); got != tc.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestLower(t *testing.T) {
	if got := Lower("DAILY \u0130"); got != "daily i\u0307" {
		t.Errorf("Lower = %q", got)
	}
}

func TestSum(t *testing.T) {
	tenths := make([]float64, 10)
	for i := range tenths {
		tenths[i] = 0.1
	}
	if got := Sum(tenths); got != 1.0 {
		t.Errorf("Sum(0.1 x 10) = %v, want 1", got)
	}
	if got := Sum([]float64{1e100, 1.0, -1e100}); got != 1.0 {
		t.Errorf("Sum = %v, want 1", got)
	}
	if got := SumSq([]float64{3, 4}); got != 25 {
		t.Errorf("SumSq = %v, want 25", got)
	}
	if got := Sum(nil); got != 0 {
		t.Errorf("Sum(nil) = %v", got)
	}
}

func TestFromISOFormat(t *testing.T) {
	d, ok := FromISOFormat("2026-03-01T07:30:15.5+02:00")
	if !ok || d.Year != 2026 || d.Month != 3 || d.Day != 1 || d.Hour != 7 || d.Minute != 30 || d.Second != 15 ||
		d.Microsecond != 500000 || !d.Aware || d.OffsetSeconds != 7200 {
		t.Errorf("FromISOFormat = %+v, %v", d, ok)
	}
	if got, want := d.Timestamp(time.UTC), float64(time.Date(2026, 3, 1, 5, 30, 15, 0, time.UTC).Unix())+0.5; got != want {
		t.Errorf("Timestamp = %v", got)
	}
	for _, bad := range []string{"", "2026-13-01", "2026-02-30T00:00", "not a date", "2026-01-01T24:00:01"} {
		if _, ok := FromISOFormat(bad); ok {
			t.Errorf("FromISOFormat(%q) accepted", bad)
		}
	}
	if d, ok := FromISOFormat("2026-01-31T24:00"); !ok || d.Month != 2 || d.Day != 1 || d.Hour != 0 {
		t.Errorf("24:00 rollover = %+v, %v", d, ok)
	}
}

func TestTimestampLocal(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	d, _ := FromISOFormat("2026-11-01T01:30")
	if got, want := d.Timestamp(loc), float64(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).Unix()); got != want {
		t.Errorf("ambiguous Timestamp = %v, want %v", got, want)
	}
}

func TestSplitTimestamp(t *testing.T) {
	for _, tc := range []struct {
		ts      float64
		sec, us int64
	}{{1.5, 1, 500000}, {-0.25, -1, 750000}, {3.25, 3, 250000}} {
		if s, u := SplitTimestamp(tc.ts); s != tc.sec || u != tc.us {
			t.Errorf("SplitTimestamp(%v) = %d, %d", tc.ts, s, u)
		}
	}
}

func TestReprBytes(t *testing.T) {
	for in, want := range map[string]string{"a'b": `b"a'b"`, `a'"b`: `b'a\'"b'`, "\x00\xff\t\\": `b'\x00\xff\t\\'`, "": "b''"} {
		if got := ReprBytes([]byte(in)); got != want {
			t.Errorf("ReprBytes(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestB64Decode(t *testing.T) {
	for in, want := range map[string]string{"AOk=": "\x00\xe9", "Q Q==": "A", "": ""} {
		if got, err := B64Decode(in); err != nil || string(got) != want {
			t.Errorf("B64Decode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"AOk":  "Incorrect padding",
		"QQ=Q": "Incorrect padding",
		"A===": "Invalid base64-encoded string: number of data characters (1) cannot be 1 more than a multiple of 4",
	} {
		if _, err := B64Decode(in); err == nil || err.Error() != want {
			t.Errorf("B64Decode(%q) err = %v, want %q", in, err, want)
		}
	}
}

func TestDecodeUTF16BE(t *testing.T) {
	if got, err := DecodeUTF16BE([]byte{0x00, 0xe9, 0xd8, 0x3d, 0xde, 0x00}); err != nil || got != "é😀" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, in := range [][]byte{{0x00}, {0xd8, 0x3d}, {0xd8, 0x3d, 0x00, 0x41}, {0xde, 0x00}} {
		if got, err := DecodeUTF16BE(in); err == nil {
			t.Errorf("DecodeUTF16BE(% x) = %q, want an error", in, got)
		}
	}
}

func TestCasefold(t *testing.T) {
	for in, want := range map[string]string{
		"Straße ÄRZTE ﬁ İ": "strasse ärzte fi i̇",
		"Sent Meßages":     "sent messages",
		"INBOX":            "inbox",
		"":                 "",
	} {
		if got := Casefold(in); got != want {
			t.Errorf("Casefold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestISOFormat(t *testing.T) {
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Skip(err)
	}
	moment := time.Date(2026, 10, 16, 9, 0, 0, 0, loc)
	if got := ISOFormat(moment, "minutes"); got != "2026-10-16T09:00-04:00" {
		t.Errorf("minutes = %q", got)
	}
	if got := ISOFormat(moment.In(time.UTC), "seconds"); got != "2026-10-16T13:00:00+00:00" {
		t.Errorf("seconds = %q", got)
	}
	odd := time.Date(1900, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 4*3600+30*60+5))
	if got := ISOFormat(odd, "seconds"); got != "1900-01-01T00:00:00+04:30:05" {
		t.Errorf("odd offset = %q", got)
	}
}

func TestDateFromISOFormat(t *testing.T) {
	raw, err := os.ReadFile("testdata/date_fromisoformat.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]*string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	valid := 0
	for in, w := range want {
		y, m, d, ok := DateFromISOFormat(in)
		got := ""
		if ok {
			got = fmt.Sprintf("%04d-%02d-%02d", y, m, d)
			valid++
		}
		if (w == nil) == ok || (w != nil && *w != got) {
			t.Errorf("DateFromISOFormat(%q) = %q, %v; Python %v", in, got, ok, w)
		}
	}
	if valid < 500 {
		t.Errorf("only %d valid cases; the corpus is too thin", valid)
	}
}

func TestUpper(t *testing.T) {
	for in, want := range map[string]string{"abc-12": "ABC-12", "straße": "STRASSE", "ǆ": "Ǆ", "ŉ": "ʼN", "ﬃ": "FFI",
		"\ua7cd\ua7db": "\ua7cc\ua7da", "\U00010d70\U00010d85": "\U00010d50\U00010d65", "\u1f80": "\u1f08\u0399",
	} {
		if got := Upper(in); got != want {
			t.Errorf("Upper(%q) = %q, want %q", in, got, want)
		}
	}
}

type moduledError struct{}

func (moduledError) Error() string    { return "boom" }
func (moduledError) PyClass() string  { return "ProposalError" }
func (moduledError) PyModule() string { return "atrium.proposals" }

type quietError struct{}

func (quietError) Error() string   { return "" }
func (quietError) PyClass() string { return "KeyError" }

func TestUncaughtText(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("plain"), "RuntimeError: plain"},
		{&Exception{Class: "ValueError", Msg: "bad"}, "ValueError: bad"},
		{moduledError{}, "atrium.proposals.ProposalError: boom"},
		{fmt.Errorf("wrapped: %w", moduledError{}), "atrium.proposals.ProposalError: wrapped: boom"},
		{quietError{}, "KeyError"},
	} {
		if got := UncaughtText(c.err); got != c.want {
			t.Errorf("UncaughtText(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
