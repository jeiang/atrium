package reviewpages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestProposalActionsOnAnUnknownIDAre404(t *testing.T) {
	s := newSite(t)
	for _, action := range []string{"approve", "dismiss", "undo"} {
		status(t, s.post("/proposals/999/"+action, nil, hx, "true"), http.StatusNotFound)
	}
	status(t, s.get("/proposals/999"), http.StatusNotFound)
	rec := s.post("/proposals/abc/approve", nil)
	body := status(t, rec, http.StatusUnprocessableEntity)
	if detail := jsonOf(t, body)["detail"].([]any)[0].(map[string]any); detail["type"] != "int_parsing" {
		t.Errorf("detail = %v", detail)
	}
}

func TestTheProposalsListIsGoneAndAProposalLinkLandsOnReview(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	status(t, s.get("/proposals"), http.StatusNotFound)
	rec := s.get(fmt.Sprintf("/proposals/%d", id))
	status(t, rec, http.StatusSeeOther)
	if got, want := rec.Header().Get("Location"), fmt.Sprintf("/review#proposal-%d", id); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestBlockedProposalsStayOnNeedsYouUntilResolved(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	s.exec("UPDATE proposals SET status = 'applied', decided_ts = 1000, applied_ts = 1000 WHERE id = ?", id)
	s.exec("INSERT INTO proposal_actions (proposal_id, ts, lease, kind, action, state, error) VALUES (?, 1000, 0, 'write', '{}', 'unknown', 'timeout')", id)
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "Blocked", fmt.Sprintf(`id="proposal-%d"`, id), fmt.Sprintf(`hx-post="/proposals/%d/resolve"`, id), "Mark checked")
	mustNotContain(t, page, "Nothing needs you.")
	if got := badge(t, page); got != "1" {
		t.Errorf("badge = %q, want 1 blocked proposal", got)
	}
	body := status(t, s.post(fmt.Sprintf("/proposals/%d/resolve", id), nil, hx, "true"), http.StatusOK)
	mustNotContain(t, body, "Mark checked")
	mustContain(t, status(t, s.get("/review"), http.StatusOK), "Nothing needs you.")
}

func TestApprovalCardShowsTheActionAndRefreshesOnlyWhilePending(t *testing.T) {
	s := newSite(t)
	preview := map[string]any{"args": map[string]any{"magnet": "magnet:?xt=urn:btih:abc", "media": true}}
	id := s.propose("media_add_magnet", "", "Add torrent", preview, "magnet")
	card := fmt.Sprintf("/proposals/%d/card", id)
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "Add torrent", "data-args", "<dt class=\"text-muted-foreground\">Magnet</dt>", "magnet:?xt=urn:btih:abc",
		"<dt class=\"text-muted-foreground\">Media</dt>", "<dd class=\"min-w-0 break-words\">Yes</dd>", ">Deny<",
		`hx-get="`+card+`?poll=1" hx-trigger="every 20s" hx-swap="outerHTML"`)
	mustNotContain(t, page, "Dismiss forever", `{&#34;args&#34;`, `"args":`)
	status(t, s.get(card+"?poll=1", hx, "true"), http.StatusNoContent)

	row := status(t, s.get(card+"?view=row", hx, "true"), http.StatusOK)
	mustContain(t, row, fmt.Sprintf(`id="needs-%d"`, id), fmt.Sprintf(`href="#proposal-%d"`, id), "Add torrent", "data-card-approve", ">Deny<")
	denied := status(t, s.post(fmt.Sprintf("/proposals/%d/dismiss", id), form("view", "row"), hx, "true"), http.StatusOK)
	mustContain(t, denied, fmt.Sprintf(`id="needs-%d"`, id), "Dismissed")
	mustNotContain(t, denied, "data-card-approve", "every 20s")

	resolved := status(t, s.get(card+"?poll=1", hx, "true"), http.StatusOK)
	mustContain(t, resolved, `data-status="dismissed"`)
	mustNotContain(t, resolved, "every 20s", "data-card-approve")
}

func TestDeletionProposalWithAnAgeConditionRenders(t *testing.T) {
	s := newSite(t)
	rule := map[string]any{"account": "icloud", "when": []any{
		map[string]any{"field": "subject", "op": "re", "value": "code"},
		map[string]any{"field": "age_days", "op": "gte", "value": int64(7)},
	}}
	preview := map[string]any{"rule": rule, "reason": "otp", "older_than_days": int64(7), "backlog": int64(1), "senders": []any{}, "subjects": []any{}, "messages": []any{message(5)}}
	s.propose("deletion_rule", "icloud", "Delete codes", preview, "del-age")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "Age in days", "is at least", "data-approve-picked", "Save rule only",
		`data-confirm-none="Save the rule without moving any messages to Trash?"`,
		`data-confirm="Move all matching messages to Trash now and save the rule?"`, "One-time codes are useless once they expire.",
		"The rule also deletes new matching mail from now on.")
	mustNotContain(t, page, "hx-confirm=", "is over")
}

func TestDeletionRuleWithoutAPickerSavesTheRuleOnly(t *testing.T) {
	s := newSite(t)
	preview := map[string]any{"rule": map[string]any{"when": []any{map[string]any{"field": "from_domain", "op": "eq", "value": "x.test"}}}, "reason": "operator_deleted", "backlog": int64(0), "senders": []any{}, "subjects": []any{}, "messages": []any{}}
	s.propose("deletion_rule", "icloud", "Delete", preview, "del-none")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, `data-confirm="Save the rule? Nothing moves to Trash now."`, "You have deleted mail like this before.", "existing messages match")
	mustNotContain(t, page, "data-approve-picked", "Approve all", "data-selection", "hx-confirm=")
}

func TestPickerCardFromARealRowShowsTheMessageChoices(t *testing.T) {
	s := newSite(t)
	s.splitProposal("a")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "data-selection", `name="msg-0"`, `name="msg-1"`, `value="4"`, `name="name-0"`, `value="Shop/Orders"`,
		"Approve all", `data-picked-total>4</span>`, `aria-label="Read Subject 1"`, `hx-get="/messages/1"`, "Choose exactly which messages move",
		`<span data-picked>2</span> of 2 messages`)
}

func TestGroupWithoutMessagesShowsItsCount(t *testing.T) {
	s := newSite(t)
	empty := map[string]any{"name": "Shop/Empty", "count": int64(3), "messages": []any{}}
	preview := map[string]any{"folder": "INBOX/Shop", "groups": []any{group("Shop/Orders", 1), empty}}
	s.propose("rebalance_split", "icloud", "Split", preview, "mixed")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "3 messages", `<span data-picked>1</span> of 1 messages`, "Approve all")
}

func TestRebalanceMergeIntoAnExistingFolderIsNotEditable(t *testing.T) {
	s := newSite(t)
	preview := map[string]any{"sources": []any{map[string]any{"folder": "Old", "count": int64(2)}}, "into": "Shop|Orders", "group": group("Shop|Orders", 1, 2)}
	id := s.propose("rebalance_merge", "icloud", "Merge", preview, "exists")
	existing := status(t, s.get(fmt.Sprintf("/proposals/%d/card", id), hx, "true"), http.StatusOK)
	mustContain(t, existing, "existing folder", "Shop · Orders")
	mustNotContain(t, existing, `name="name-0"`)
}

func TestCalendarAndNotionCardsToleratePartialPreviews(t *testing.T) {
	s := newSite(t)
	s.propose("calendar_event", "", "Lunch", map[string]any{"calendar": "google", "all_day": true, "start": "2026-11-01", "end": "2026-11-02"}, "cal")
	s.propose("notion_link", "", "Link", map[string]any{"a": map[string]any{"id": "p-a", "title": "A", "url": "https://notion.so/a"}, "b": map[string]any{"id": "p-b", "title": nil, "url": "https://notion.so/b"}}, "link")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "Google", "<dt class=\"text-muted-foreground\">Date</dt>", "2026-11-01", "Nobody is invited", "Untitled",
		`href="/notion/p-a"`, `aria-label="Open Untitled in Notion"`)
	mustNotContain(t, page, "Where", "Notes", "<blockquote")
}

func TestFailedAndAppliedFootersByKind(t *testing.T) {
	s := newSite(t)
	failed := s.splitProposal("failed")
	s.exec("UPDATE proposals SET status = 'failed', decided_ts = 1000, result = ? WHERE id = ?", `{"error": "boom <b>"}`, failed)
	applied := s.propose("reminder", "", "Remind", map[string]any{"text": "call", "due": "2026-11-01", "remind_at": "09:00"}, "rem")
	s.exec("UPDATE proposals SET status = 'applied', applied_ts = 1000, decided_ts = 1000 WHERE id = ?", applied)
	var page strings.Builder
	for _, id := range []int64{failed, applied} {
		page.WriteString(status(t, s.get(fmt.Sprintf("/proposals/%d/card", id), hx, "true"), http.StatusOK))
	}
	mustContain(t, page.String(), "boom &lt;b&gt;", "Tried", ">Applied<")
	mustNotContain(t, page.String(), "Undo what it did", "hx-confirm=", "every 20s")
}

func TestShadowModeDeletionRuleCopy(t *testing.T) {
	s := newSite(t, func(env map[string]string) { env["ATRIUM_MODE"] = "shadow" })
	preview := map[string]any{"rule": map[string]any{"when": []any{}}, "reason": "otp", "backlog": int64(1), "senders": []any{}, "subjects": []any{}, "messages": []any{message(1)}}
	s.propose("deletion_rule", "icloud", "Delete", preview, "d")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "only once routing is live", "existing message matches")
}

func TestUndoOfAProposalThatIsNotAppliedReportsNotUndone(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	body := status(t, s.post("/proposals/"+itoa(int(id))+"/undo", nil, hx, "true"), http.StatusOK)
	mustContain(t, body, "Not undone", "is not applied")
}

func TestTriageProposalRendersTheMessagePicker(t *testing.T) {
	s := newSite(t)
	preview := map[string]any{"count": int64(1), "messages": []any{map[string]any{"id": int64(5), "subject": "Quarterly invoice", "action": "archive"}}}
	s.propose("mail_triage", "icloud", "Triage 1 messages", preview, "triage")
	page := status(t, s.get("/review"), http.StatusOK)
	mustContain(t, page, "Mail triage", "Triage 1 messages", "Quarterly invoice", "Approve all")
}
