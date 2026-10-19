package httpx

import (
	"fmt"
	"testing"
)

func TestMessageDropsTheClassName(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: embedding response shape mismatch", ErrValue), "embedding response shape mismatch"},
		{fmt.Errorf("%w: 'data'", ErrKey), "'data'"},
		{&ServiceUnavailable{Msg: "offline"}, "offline"},
	} {
		if got := Message(c.err); got != c.want {
			t.Errorf("Message(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
