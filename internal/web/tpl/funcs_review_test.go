package tpl

import (
	"testing"

	"github.com/jeiang/atrium/internal/py"
)

func TestPyScalarPrintsAsPythonStr(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "None"}, {true, "True"}, {false, "False"}, {int64(7), "7"}, {1759996400.0, "1759996400.0"}, {0.25, "0.25"},
		{1e16, "1e+16"}, {"text", "text"}, {[]any{"a", int64(1)}, "['a', 1]"},
	} {
		if got, err := pyScalar(c.in); err != nil || got != c.want {
			t.Errorf("pyScalar(%#v) = %q, %v, want %q", c.in, got, err, c.want)
		}
	}
}

func TestPyAttrDistinguishesAbsentFromNull(t *testing.T) {
	m := map[string]any{"none": nil, "n": int64(3)}
	ordered := py.Dict{{Key: "x", Value: 2.0}}
	for _, c := range []struct {
		in   any
		key  string
		want string
	}{
		{m, "none", "None"}, {m, "n", "3"}, {m, "absent", ""}, {ordered, "x", "2.0"}, {ordered, "y", ""}, {nil, "x", ""},
	} {
		if got, err := pyAttr(c.in, c.key); err != nil || got != c.want {
			t.Errorf("pyAttr(%v, %s) = %q, %v, want %q", c.in, c.key, got, err, c.want)
		}
	}
}

func TestPyLenIsZeroForMissingValues(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int
	}{
		{nil, 0}, {[]any{1, 2}, 2}, {map[string]any{"a": 1}, 1}, {py.Dict{{Key: "a"}}, 1}, {"héé", 3}, {5, 0},
	} {
		if got := pyLen(c.in); got != c.want {
			t.Errorf("pyLen(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
}
