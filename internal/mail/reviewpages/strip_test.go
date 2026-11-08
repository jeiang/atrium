package reviewpages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
)

func TestNeedsStripIsEmptyWithNothingPending(t *testing.T) {
	s := newSite(t)
	body := status(t, s.get("/review/strip"), http.StatusOK)
	if strings.TrimSpace(body) != "" {
		t.Fatalf("empty strip rendered %q", body)
	}
}

func TestNeedsStripShowsAtMostThreeInReviewOrder(t *testing.T) {
	s := newSite(t)
	older := s.propose("rebalance_split", "icloud", "Split Older", map[string]any{"folder": "INBOX/Older", "groups": []any{group("Older/A", 5)}}, "older")
	newer := s.propose("rebalance_split", "icloud", "Split Newer", map[string]any{"folder": "INBOX/Newer", "groups": []any{group("Newer/A", 6)}}, "newer")
	threaded := s.draft(config.Gmail, "a@x.test", nil, "Threaded reply", "body", nil)
	s.exec("UPDATE drafts SET thread_id = 42, updated_at = 2000 WHERE id = ?", threaded)
	loose := s.draft(config.Gmail, "b@x.test", nil, "Loose reply", "body", nil)
	s.exec("UPDATE drafts SET updated_at = 1000 WHERE id = ?", loose)

	body := status(t, s.get("/review/strip"), http.StatusOK)
	if n := strings.Count(body, "data-needs-item"); n != 3 {
		t.Fatalf("%d strip items, want 3: %s", n, body)
	}
	mustContain(t, body, "data-needs-strip", "Needs you", `href="/review" class=`, ">4</span>",
		fmt.Sprintf(`href="/proposals/%d"`, newer), fmt.Sprintf(`href="/proposals/%d"`, older), `href="/chat/42"`, "Threaded reply")
	mustNotContain(t, body, "Loose reply")
	if strings.Index(body, "Split Newer") > strings.Index(body, "Split Older") || strings.Index(body, "Split Older") > strings.Index(body, "Threaded reply") {
		t.Fatalf("strip order: %s", body)
	}

	s.exec("UPDATE proposals SET status = 'dismissed'")
	s.exec("UPDATE drafts SET status = 'sent' WHERE id = ?", threaded)
	body = status(t, s.get("/review/strip"), http.StatusOK)
	mustContain(t, body, fmt.Sprintf(`href="/review#draft-%d"`, loose), "Loose reply", ">1</span>")
	if n := strings.Count(body, "data-needs-item"); n != 1 {
		t.Fatalf("%d strip items, want 1", n)
	}
}
