package live

import "testing"

func TestStateOf(t *testing.T) {
	zero, one, two := int64(0), int64(1), int64(2)
	undone := 5.0
	for _, c := range []struct {
		ok   *int64
		undo *float64
		want string
	}{
		{&one, nil, Applied}, {&zero, nil, Skipped}, {nil, nil, Held}, {&two, nil, Held},
		{&one, &undone, Undone}, {nil, &undone, Undone},
	} {
		if got := StateOf(c.ok, c.undo); got != c.want {
			t.Errorf("StateOf(%v, %v) = %q, want %q", c.ok, c.undo, got, c.want)
		}
	}
}
