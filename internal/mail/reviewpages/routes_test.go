package reviewpages

import (
	"net/http"
	"strings"
	"testing"
)

func TestWrongMethodsAreRefused(t *testing.T) {
	s := newSite(t)
	status(t, s.do(http.MethodPost, "/proposals/1", nil), http.StatusMethodNotAllowed)
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
func TestEveryReviewPageMarksTheHubAndItsTabCurrent(t *testing.T) {
	s := newSite(t)
	for _, c := range []struct{ target, tab, heading string }{
		{"/review", "Needs you", "Needs you"}, {"/loops", "Loops", "Open loops"},
		{"/watches", "Packages", "Packages"}, {"/activity", "Done", "Done"},
	} {
		page := status(t, s.get(c.target), http.StatusOK)
		if n := strings.Count(page, `aria-current="page"`); n != 2 {
			t.Errorf("%s marks %d entries current", c.target, n)
		}
		hub := between(page, `class="sb-item" title="Review" aria-current="page"`, "</a>")
		if !strings.Contains(text(hub), "Review") {
			t.Errorf("%s does not mark the Review hub current", c.target)
		}
		tab := between(page, `class="hub-tab" aria-current="page">`, "</a>")
		if text(tab) != c.tab {
			t.Errorf("%s marks tab %q current, want %s", c.target, text(tab), c.tab)
		}
		mustContain(t, page, `<h1>`+c.heading+`</h1>`, `href="/review"`, `href="/loops"`, `href="/watches"`, `href="/activity"`)
	}
	page := status(t, s.get("/drafts"), http.StatusOK)
	mustNotContain(t, page, `aria-current="page"`, `class="hub-tabs"`)
}
