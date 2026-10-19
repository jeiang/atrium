package py

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestIntString(t *testing.T) {
	for in, want := range map[string]int64{
		"1_0": 10, " 7 ": 7, "\u0663": 3, "-1": -1, "+1_000": 1000, "\t-0\n": 0, "\u0661\u0662": 12, "007": 7,
	} {
		got, err := Int(in)
		if err != nil || !got.IsInt64() || got.Int64() != want {
			t.Errorf("Int(%q) = %v, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"+_1", "-_1", "_1", "1_", "1__0", "", " ", "+", "- 1", "1.0", "0x10", "1e3", "١_"} {
		_, err := Int(in)
		var e *Exception
		if !errors.As(err, &e) || e.Class != "ValueError" || e.Msg != "invalid literal for int() with base 10: "+Repr(in) {
			t.Errorf("Int(%q) = %v, want ValueError", in, err)
		}
	}
	huge, err := Int("123456789012345678901234567890")
	if want, _ := new(big.Int).SetString("123456789012345678901234567890", 10); err != nil || huge.Cmp(want) != 0 {
		t.Errorf("Int(huge) = %v, %v", huge, err)
	}
}

func TestIntOfValues(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int64
	}{{true, 1}, {false, 0}, {int64(5), 5}, {uint32(6), 6}, {7, 7}, {-2.9, -2}, {big.NewInt(9), 9}} {
		got, err := Int(c.in)
		if err != nil || got.Int64() != c.want {
			t.Errorf("Int(%v) = %v, %v", c.in, got, err)
		}
	}
	for _, c := range []struct {
		in          any
		class, text string
	}{
		{math.NaN(), "ValueError", "cannot convert float NaN to integer"},
		{math.Inf(1), "OverflowError", "cannot convert float infinity to integer"},
		{nil, "TypeError", "int() argument must be a string, a bytes-like object or a real number, not 'NoneType'"},
		{[]any{}, "TypeError", "int() argument must be a string, a bytes-like object or a real number, not 'list'"},
		{Dict{}, "TypeError", "int() argument must be a string, a bytes-like object or a real number, not 'dict'"},
	} {
		_, err := Int(c.in)
		if ExceptionText(err) != c.class+": "+c.text {
			t.Errorf("Int(%v) = %v", c.in, err)
		}
	}
}

func TestIter(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{{[]any{1, 2}, 2}, {[]string{"a"}, 1}, {[]int64{1, 2, 3}, 3}, {[]uint32{4}, 1}, {[]int{}, 0}, {"ab", 2}, {Dict{{Key: "k", Value: 1}}, 1}, {map[string]any{"a": 1, "b": 2}, 2}} {
		got, err := Iter(c.in)
		if err != nil || len(got) != c.want {
			t.Errorf("Iter(%v) = %v, %v", c.in, got, err)
		}
	}
	if got, _ := Iter([]int64{7}); got[0] != int64(7) {
		t.Errorf("Iter([]int64) = %v", got)
	}
	for in, name := range map[any]string{int64(1): "int", 1.5: "float", true: "bool"} {
		if _, err := Iter(in); ExceptionText(err) != "TypeError: '"+name+"' object is not iterable" {
			t.Errorf("Iter(%v) = %v", in, err)
		}
	}
	if _, err := Iter(nil); ExceptionText(err) != "TypeError: 'NoneType' object is not iterable" {
		t.Errorf("Iter(nil) = %v", err)
	}
}

func TestTypeNameNamesEveryValue(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{{[]int64{}, "list"}, {[]uint32{}, "list"}, {uint32(1), "int"}, {map[string]any{}, "dict"}, {Dict{}, "dict"}} {
		if got := TypeName(c.in); got != c.want {
			t.Errorf("TypeName(%T) = %q", c.in, got)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("TypeName of a Go struct did not panic")
		}
	}()
	TypeName(struct{}{})
}

func TestLaxInt(t *testing.T) {
	for in, want := range map[string]int64{
		"5": 5, "+5": 5, " 5 ": 5, "5.0": 5, "5.00": 5, "-0.0": 0, "1_0": 10, "1_0.0": 10, "\t7\n": 7, "\u00a07": 7, "-3": -3, "007": 7,
	} {
		if got, ok := LaxInt64(in); !ok || got != want {
			t.Errorf("LaxInt64(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", " ", "+", "-", "1e3", "1.5e1", "0x1p4", ".0", "5.", "5.0_0", "5.5", "1__0", "_1", "1_", "\u0663", "--5", "+-5", "5-", "1 0", "1_.0", "1._0",
		"\x1c7", "\x1f7", "7\x1c", "inf", "nan", "99999999999999999999",
	} {
		if got, ok := LaxInt64(in); ok {
			t.Errorf("LaxInt64(%q) = %d, accepted", in, got)
		}
	}
	if n, ok := LaxInt("99999999999999999999"); !ok || n.String() != "99999999999999999999" {
		t.Errorf("LaxInt keeps a value past int64: %v, %v", n, ok)
	}
}

func TestIsDigits(t *testing.T) {
	for in, want := range map[string]bool{"": false, "30": true, "\u0663\u0660": true, "\u00b2": true, "3.0": false, "-3": false, " 3": false, "a": false} {
		if got := IsDigits(in); got != want {
			t.Errorf("IsDigits(%q) = %v", in, got)
		}
	}
}
