package connections

import "testing"

func TestRequestOutcomesReachOnlyTheirOwnAppsHealthWatch(t *testing.T) {
	mine, theirs := &Registry{}, &Registry{}
	got, stopMine := Outcomes(mine)
	defer stopMine()
	other, stopTheirs := Outcomes(theirs)
	defer stopTheirs()

	report(mine, "service/tmdb", 1, false)
	report(&Registry{}, "service/tmdb", 1, false)

	select {
	case o := <-got:
		if o.ID != "service/tmdb" || o.OK {
			t.Fatalf("outcome %+v, want a service/tmdb failure", o)
		}
	default:
		t.Fatal("the failure did not reach its own app's health watch")
	}
	select {
	case o := <-other:
		t.Fatalf("another app's health watch received %+v", o)
	case o := <-got:
		t.Fatalf("an unregistered app's outcome reached this watch: %+v", o)
	default:
	}
}
