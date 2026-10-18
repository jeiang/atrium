package anthropic

import "testing"

// Expected values are Python's f"{json.loads(text)}".
func TestPyRepr(t *testing.T) {
	cases := map[string]string{
		`{"a": [1, -0, 2.5, 1.0, 1e16, 1.5e-5, 0.0001, 123456789012345678901234567890, -0.0, 1E2]}`: `{'a': [1, 0, 2.5, 1.0, 1e+16, 1.5e-05, 0.0001, 123456789012345678901234567890, -0.0, 100.0]}`,
		`{"t": true, "f": false, "n": null, "e": {}, "l": []}`:                                      `{'t': True, 'f': False, 'n': None, 'e': {}, 'l': []}`,
		`["it's"]`:                          `["it's"]`,
		`["both ' and \""]`:                 `['both \' and "']`,
		`["tab\tnl\n\\ é \u0001 \u200b 😀"]`: `['tab\tnl\n\\ é \x01 \u200b 😀']`,
		`"bare"`:                            `bare`,
		`12345678901234567.0`:               `1.2345678901234568e+16`,
		`0.1`:                               `0.1`,
	}
	for in, want := range cases {
		got, ok := pyRepr([]byte(in))
		if !ok || got != want {
			t.Errorf("pyRepr(%s) = %s, want %s", in, got, want)
		}
	}
	if _, ok := pyRepr([]byte("not json")); ok {
		t.Error("pyRepr accepted invalid JSON")
	}
}
