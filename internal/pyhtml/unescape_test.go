package pyhtml

import "testing"

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		"&amp;&ampx&copy;&#233;&#x110000;&#128;&#0;&#1;&notit;&bogus;&#;&#x;&": "&&x©é\ufffd€\ufffd¬it;&bogus;&#;&#x;&",
		"&lt&gt&amp":  "<>&",
		"no entities": "no entities",
	} {
		if got := Unescape(in); got != want {
			t.Errorf("Unescape(%q) = %q, want %q", in, got, want)
		}
	}
}
