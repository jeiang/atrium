package py

import (
	"math"
	"math/big"
	"testing"
)

func TestFloatRepr(t *testing.T) {
	for in, want := range map[float64]string{
		0: "0.0", 7: "7.0", 7.5: "7.5", -0.25: "-0.25", 30: "30.0", 1e15: "1000000000000000.0", 1e16: "1e+16",
		1.5e-5: "1.5e-05", 0.0001: "0.0001", 123456789.125: "123456789.125", 0.30000000000000004: "0.30000000000000004",
	} {
		if got := FloatRepr(in); got != want {
			t.Errorf("FloatRepr(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestReprValue(t *testing.T) {
	v := Dict{{"a", []any{int64(1), 2.5, "it's", nil, true}}, {"b", new(big.Int).Lsh(big.NewInt(1), 70)}}
	want := `{'a': [1, 2.5, "it's", None, True], 'b': 1180591620717411303424}`
	if got := ReprValue(v); got != want {
		t.Errorf("ReprValue = %s, want %s", got, want)
	}
	if got := ReprValue(map[string]any{"z": "x", "a": []string{"q"}}); got != `{'a': ['q'], 'z': 'x'}` {
		t.Errorf("map repr %s", got)
	}
}

func TestJSONLoads(t *testing.T) {
	v, err := JSONLoads([]byte(` {"b": 1, "a": [1.0, -0, 1e2, "x"], "b": {"k": null}, "big": 123456789012345678901234567890} `))
	if err != nil {
		t.Fatal(err)
	}
	want := `{'b': {'k': None}, 'a': [1.0, 0, 100.0, 'x'], 'big': 123456789012345678901234567890}`
	if got := ReprValue(v); got != want {
		t.Errorf("JSONLoads = %s, want %s", got, want)
	}
	special, err := JSONLoads([]byte(`[NaN, Infinity, -Infinity, 1e400, "\ud83d\ude00 \u00e9"]`))
	if err != nil || ReprValue(special) != `[nan, inf, -inf, inf, '😀 é']` {
		t.Errorf("specials %s, %v", ReprValue(special), err)
	}
	for _, bad := range []string{``, `{`, `[1,]`, `1 2`, `{"a": }`, `01`, "\"a\nb\"", `[1 2]`, `{1: 2}`, `nan`} {
		if _, err := JSONLoads([]byte(bad)); err == nil {
			t.Errorf("JSONLoads(%q) succeeded", bad)
		}
	}
}

func TestDictGetSet(t *testing.T) {
	d := Dict{{"a", 1}}
	e := d.Set("a", 2).Set("b", 3)
	if v, _ := d.Get("a"); v != 1 {
		t.Error("Set changed the receiver")
	}
	if v, ok := e.Get("a"); !ok || v != 2 {
		t.Errorf("a = %v", v)
	}
	if _, ok := e.Get("c"); ok || len(e) != 2 {
		t.Errorf("dict %v", e)
	}
}

func TestJSONDumpsFloats(t *testing.T) {
	got := JSONDumps([]any{7.5, 2.0, nanValue(), posInf(), -posInf(), new(big.Int).Lsh(big.NewInt(1), 70)})
	if want := `[7.5, 2.0, NaN, Infinity, -Infinity, 1180591620717411303424]`; got != want {
		t.Errorf("JSONDumps = %s, want %s", got, want)
	}
}

func nanValue() float64 { return math.NaN() }
func posInf() float64   { return math.Inf(1) }
