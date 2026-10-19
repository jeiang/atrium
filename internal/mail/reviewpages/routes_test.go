package reviewpages

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/web"
)

// inventoryRoutes are the routes of slice 06 sections 3.6 (proposals), 3.7 (activity), 3.8 (drafts) and 3.13 (loops,
// watches) of the parity inventory.
var inventoryRoutes = []string{
	"GET /proposals",
	"POST /proposals/{proposal_id}/approve",
	"POST /proposals/{proposal_id}/dismiss",
	"POST /proposals/{proposal_id}/undo",
	"GET /activity",
	"POST /activity/mail/{action_id}/undo",
	"POST /activity/write/{write_id}/undo",
	"GET /drafts",
	"GET /drafts/{draft_id}",
	"POST /drafts/{draft_id}",
	"POST /drafts/{draft_id}/discard",
	"POST /drafts/{draft_id}/send",
	"POST /drafts/{draft_id}/resolve",
	"GET /loops",
	"POST /loops/{loop_id}/snooze",
	"POST /loops/{loop_id}/dismiss",
	"POST /loops/dismiss-all",
	"POST /loops/commitments/{commitment_id}/{outcome}",
	"GET /watches",
	"POST /watches/packages/{package_id}/dismiss",
	"POST /watches/birthdays/dismiss",
}

func TestRouteTableMatchesTheInventory(t *testing.T) {
	s := newSite(t)
	router := chi.NewRouter()
	Module(Options{})(router, &web.Deps{Cfg: s.cfg, Env: s.env, DB: s.db})
	var got []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(inventoryRoutes)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("routes\n got %v\nwant %v", got, want)
	}
	if len(got) != 21 {
		t.Errorf("%d routes, want 21", len(got))
	}
}

func TestWrongMethodsAreRefused(t *testing.T) {
	s := newSite(t)
	status(t, s.do(http.MethodPost, "/proposals", nil), http.StatusMethodNotAllowed)
	status(t, s.do(http.MethodGet, "/proposals/1/approve", nil), http.StatusMethodNotAllowed)
	status(t, s.do(http.MethodDelete, "/drafts/1", nil, "X-CSRF-Token", s.token), http.StatusMethodNotAllowed)
}

func TestPostsWithoutTheCSRFTokenAreForbidden(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	s.token = ""
	status(t, s.do(http.MethodPost, "/proposals/"+itoa(int(id))+"/approve", form("all", "1")), http.StatusForbidden)
	if got := s.proposal(id).Status; got != "pending" {
		t.Errorf("status = %s", got)
	}
}
func TestEveryPageMarksItsSidebarEntryCurrent(t *testing.T) {
	s := newSite(t)
	for _, c := range []struct{ target, label, heading string }{
		{"/proposals", "Proposals", "Proposals"}, {"/activity", "Activity", "Activity"}, {"/drafts", "Drafts", "Drafts"},
		{"/loops", "Loops", "Open loops"}, {"/watches", "Watches", "Watches"},
	} {
		page := status(t, s.get(c.target), http.StatusOK)
		if n := strings.Count(page, `aria-current="page"`); n != 1 {
			t.Errorf("%s marks %d entries current", c.target, n)
		}
		current := between(page, `aria-current="page"`, "</a>")
		if !strings.Contains(text(current), c.label) {
			t.Errorf("%s marks %q current, want %s", c.target, text(current), c.label)
		}
		mustContain(t, page, `<h1 class="truncate px-1 text-sm font-medium">`+c.heading+`</h1>`)
	}
}
