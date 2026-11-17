package application

import (
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/connections"
)

func TestASiblingSuccessDoesNotRecheckAFailingConnectionInsideItsBackoff(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	view := func(state connections.State, checked, lastOK *time.Time) View {
		return View{ID: "icloud/home", Enabled: true, State: state, CheckedAt: checked, LastOKAt: lastOK}
	}
	recovered := ObserveHealthInput{ID: "icloud/home", Recovered: true}
	failure := ObserveHealthInput{ID: "icloud/home"}
	cases := []struct {
		name          string
		v             View
		recheck, boot bool
	}{
		{"failing for minutes, checked 2 min ago", view(connections.Failing, at(2*time.Minute), at(10*time.Minute)), false, false},
		{"failing for minutes, checked 61 min ago", view(connections.Failing, at(61*time.Minute), at(70*time.Minute)), true, true},
		{"failing for 2 h, checked 2 h ago", view(connections.Failing, at(2*time.Hour), at(4*time.Hour)), false, false},
		{"failing for 2 h, checked 3 h ago", view(connections.Failing, at(3*time.Hour+time.Minute), at(5*time.Hour)), true, true},
		{"failing for 8 h, checked 11 h ago", view(connections.Failing, at(11*time.Hour), at(19*time.Hour)), false, false},
		{"failing for 8 h, checked 13 h ago", view(connections.Failing, at(13*time.Hour), at(21*time.Hour)), true, true},
		{"never ok, checked 30 min ago", view(connections.Failing, at(30*time.Minute), nil), false, false},
		{"needs reauth for 4 h by its reauth time", View{ID: "icloud/home", Enabled: true, State: connections.NeedsReauth, CheckedAt: at(5 * time.Hour), ReauthAt: at(9 * time.Hour)}, false, false},
	}
	for _, c := range cases {
		if got := wanted(c.v, recovered); got != c.recheck {
			t.Errorf("%s: a sibling success rechecks = %v, want %v", c.name, got, c.recheck)
		}
		if got := wanted(c.v, failure); got != c.recheck {
			t.Errorf("%s: a runtime failure rechecks = %v, want %v", c.name, got, c.recheck)
		}
		if got := due(c.v, true); got != c.boot {
			t.Errorf("%s: a boot rechecks = %v, want %v", c.name, got, c.boot)
		}
	}
	healthy := view(connections.OK, at(6*time.Minute), at(6*time.Minute))
	if !wanted(healthy, failure) || wanted(healthy, recovered) {
		t.Error("a healthy connection must still be checked at once after a runtime failure and not after a success")
	}
}
