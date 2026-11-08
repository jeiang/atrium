package pages

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestWelcomeHeroShowsOnlyBeforeTheFirstThread(t *testing.T) {
	s := newSite(t)
	fresh := status(t, s.get("/chat"), http.StatusOK)
	mustContain(t, fresh, "data-welcome", "What can I take off your plate?", `data-action="fill-composer" data-target="new-text"`, `data-text="Plan my week"`, `hx-get="/review/strip"`)
	if n := strings.Count(fresh, `data-action="fill-composer"`); n != 3 {
		t.Fatalf("%d starter suggestions, want 3", n)
	}
	s.newThread("First", 0)
	home := status(t, s.get("/chat"), http.StatusOK)
	mustNotContain(t, home, "data-welcome", "What can I take off your plate?")
	mustContain(t, home, "First", `hx-get="/review/strip"`)
}

func TestTodayRedirectsToOneThreadPerDay(t *testing.T) {
	s := newSite(t)
	first := s.get("/today")
	second := s.get("/today")
	if first.StatusCode != http.StatusSeeOther || second.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, %d", first.StatusCode, second.StatusCode)
	}
	at := first.Header.Get("Location")
	if !strings.HasPrefix(at, "/chat/") || second.Header.Get("Location") != at {
		t.Fatalf("locations %q, %q", at, second.Header.Get("Location"))
	}
	if n := s.count("SELECT COUNT(*) FROM threads WHERE role = 'today'"); n != 1 {
		t.Fatalf("%d today threads, want 1", n)
	}
	mustContain(t, status(t, s.get(at), http.StatusOK), "Today · ")

	requests := make([]*http.Request, 8)
	for i := range requests {
		requests[i] = s.request(http.MethodGet, "/today", nil, "")
	}
	locations := make([]string, len(requests))
	errs := make([]error, len(requests))
	var wg sync.WaitGroup
	for i, req := range requests {
		wg.Go(func() {
			resp, err := noRedirect.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			_ = resp.Body.Close()
			locations[i] = resp.Header.Get("Location")
		})
	}
	wg.Wait()
	for i := range requests {
		if errs[i] != nil || locations[i] != at {
			t.Fatalf("concurrent visit %d: %v %q", i, errs[i], locations[i])
		}
	}
	if n := s.count("SELECT COUNT(*) FROM threads WHERE role = 'today'"); n != 1 {
		t.Fatalf("%d today threads after concurrent visits, want 1", n)
	}

	s.mu.Lock()
	s.clock += 86400
	s.mu.Unlock()
	next := s.get("/today").Header.Get("Location")
	if next == at || !strings.HasPrefix(next, "/chat/") {
		t.Fatalf("next day location %q", next)
	}
	if n := s.count("SELECT COUNT(*) FROM threads WHERE role = 'today'"); n != 2 {
		t.Fatalf("%d today threads after a day, want 2", n)
	}
}

var scriptTag = regexp.MustCompile(`<script[^>]*>`)

func TestCommandPaletteMarkupAndThreads(t *testing.T) {
	s := newSite(t)
	id := s.newThread("Quarterly taxes", 0)
	page := status(t, s.get("/chat"), http.StatusOK)
	mustContain(t, page, `<dialog id="palette"`, `data-palette-threads="/chat/palette"`, `role="combobox"`, `aria-controls="palette-list"`, `role="listbox"`,
		`id="palette-open"`, `title="Search and commands (⌘K)"`, `data-palette-href="/today"`, `data-palette-theme="dark"`, "data-palette-sidebar", `href="/today"`)
	tags := scriptTag.FindAllString(page, -1)
	if len(tags) == 0 {
		t.Fatal("no script tags")
	}
	app := false
	for _, tag := range tags {
		if !strings.Contains(tag, `src="/static/`) {
			t.Fatalf("script not loaded from /static: %s", tag)
		}
		app = app || strings.Contains(tag, "/static/app")
	}
	if !app {
		t.Fatalf("app.js not loaded: %v", tags)
	}

	found := status(t, s.get("/chat/palette?q=quarterly"), http.StatusOK)
	mustContain(t, found, fmt.Sprintf(`data-palette-href="/chat/%d"`, id), "Quarterly taxes", `role="option"`)
	missing := status(t, s.get("/chat/palette?q=zebra"), http.StatusOK)
	mustNotContain(t, missing, `role="option"`)
}
