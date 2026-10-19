package py

import (
	"math"
	"testing"
)

func TestParseFloat(t *testing.T) {
	for in, want := range map[string]float64{
		"75": 75, " 75.5\n": 75.5, "1_0.5e1_0": 10.5e10, "-INF": math.Inf(-1), "+1e400": math.Inf(1), ".5": 0.5, "5.": 5,
		"Infinity": math.Inf(1), "1E5": 1e5,
	} {
		got, err := ParseFloat(in)
		if err != nil || got != want {
			t.Errorf("ParseFloat(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := ParseFloat("NaN"); err != nil || !math.IsNaN(got) {
		t.Errorf("ParseFloat(NaN) = %v, %v", got, err)
	}
	for _, in := range []string{"", "abc", "0x10", "1__0", "_1", "1_", "1e", "e5", ".", "1 2", "--1", "1_.5", "infinit", "1e5_"} {
		_, err := ParseFloat(in)
		want := "could not convert string to float: " + Repr(in)
		if err == nil || err.Error() != want {
			t.Errorf("ParseFloat(%q) error = %v, want %s", in, err, want)
		}
	}
}

func TestParseInt(t *testing.T) {
	for in, want := range map[string]int64{"200000": 200000, " 90_000 ": 90000, "-5": -5, "+7": 7, "007": 7} {
		if got, ok := ParseInt(in); !ok || got != want {
			t.Errorf("ParseInt(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "1.5", "1e3", "0x1", "--1", "1__0", "9223372036854775808", "abc", "_1"} {
		if _, ok := ParseInt(in); ok {
			t.Errorf("ParseInt(%q) accepted", in)
		}
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{FormatFixed(60, 2), "60.00"},
		{FormatFixed(0.125, 2), "0.12"},
		{FormatFixed(math.Inf(1), 2), "inf"},
		{FormatFixed(math.NaN(), 2), "nan"},
		{FormatPercent(0.8, 0), "80%"},
		{FormatPercent(0.005, 0), "0%"},
		{FormatPercent(0.995, 0), "100%"},
		{FormatPercent(1, 0), "100%"},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}
