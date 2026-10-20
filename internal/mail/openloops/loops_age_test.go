package openloops_test

import (
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/mail/openloops"
)

func TestWaitingLoopsExpireAfterSixtyDays(t *testing.T) {
	d := newDB(t)
	sent(t, d, 1, 59, "pat@person.test", nil)
	compute(t, d, NOW())
	if got := stateList(t, d); !slices.Equal(got, []string{"waiting_on_them"}) {
		t.Errorf("a 59 day old sent message gave %v", got)
	}
	d = newDB(t)
	sent(t, d, 1, 61, "pat@person.test", nil)
	if got := compute(t, d, NOW()); got != (openloops.Counts{}) {
		t.Errorf("a 61 day old sent message gave %+v", got)
	}
	if got := stateList(t, d); len(got) != 0 {
		t.Errorf("states = %v", got)
	}
}
