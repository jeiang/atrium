package reviewpages

import (
	"net/http"
	"strings"
	"testing"
)

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
