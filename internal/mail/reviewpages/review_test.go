package reviewpages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
)

func badge(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `id="pending-count"`)
	if !ok {
		t.Fatalf("no pending badge: %s", body)
	}
	_, rest, _ = strings.Cut(rest, ">")
	count, _, _ := strings.Cut(rest, "<")
	return count
}

func TestNeedsYouListsPendingProposalsAndOpenDrafts(t *testing.T) {
	s := newSite(t)
	empty := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, empty, "<h1>Needs you</h1>", "Nothing needs you.")
	if got := badge(t, empty); got != "" {
		t.Errorf("empty badge = %q", got)
	}

	pending := s.splitProposal("a")
	dismissed := s.propose("rebalance_split", "icloud", "Split Gone", map[string]any{"folder": "INBOX/Gone", "groups": []any{group("Gone/A", 5)}}, "gone")
	status(t, s.post(fmt.Sprintf("/proposals/%d/dismiss", dismissed), nil, hx, "true"), http.StatusOK)
	open := s.draft(config.Gmail, "a@x.test", nil, "Open draft", "body", nil)
	unknown := s.draft(config.Gmail, "b@x.test", nil, "Unknown draft", "body", nil)
	s.exec("UPDATE drafts SET status = 'unknown' WHERE id = ?", unknown)
	sent := s.draft(config.Gmail, "c@x.test", nil, "Sent draft", "body", nil)
	s.exec("UPDATE drafts SET status = 'sent' WHERE id = ?", sent)

	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, fmt.Sprintf(`id="proposal-%d"`, pending), "Split Shop",
		fmt.Sprintf(`id="draft-%d"`, open), "Open draft", fmt.Sprintf(`id="draft-%d"`, unknown), "Check if the mail went out")
	mustNotContain(t, page, "Split Gone", "Sent draft", "Nothing needs you.")
	if got := badge(t, page); got != "3" {
		t.Errorf("badge = %q, want 3 (1 pending proposal + 2 open drafts)", got)
	}

	discarded := status(t, s.post(fmt.Sprintf("/drafts/%d/discard", open), nil, hx, "true"), http.StatusOK)
	mustContain(t, discarded, `hx-swap-oob="true"`)
	if got := badge(t, discarded); got != "2" {
		t.Errorf("badge after discard = %q, want 2", got)
	}
	settled := status(t, s.post(fmt.Sprintf("/proposals/%d/dismiss", pending), nil, hx, "true"), http.StatusOK)
	if got := badge(t, settled); got != "1" {
		t.Errorf("badge after dismissing the proposal = %q, want 1", got)
	}
}
