package reviewpages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func (s *site) recording(kind string, seen map[string]string) {
	s.registry.Executors[kind] = func(_ context.Context, _ *proposals.Proposal, selection any) (py.Dict, error) {
		if selection == nil {
			seen[kind] = "None"
		} else {
			seen[kind] = py.JSONDumps(selection)
		}
		return py.Dict{{Key: "summary", Value: "moved"}}, nil
	}
}

func TestApproveMapsTheFormSelectionToGroups(t *testing.T) {
	s := newSite(t)
	seen := map[string]string{}
	s.recording("rebalance_split", seen)
	id := s.splitProposal("split-1")
	rec := s.post("/proposals/"+itoa(int(id))+"/approve",
		form("name-0", "Shop/Orders2", "msg-0", "1", "msg-0", "2", "name-1", "Shop/Ads", "msg-1", "4"), hx, "true")
	body := status(t, rec, http.StatusOK)
	want := `{"groups": [{"name": "Shop/Orders2", "message_ids": [1, 2]}, {"name": "Shop/Ads", "message_ids": [4]}]}`
	if seen["rebalance_split"] != want {
		t.Errorf("selection = %s, want %s", seen["rebalance_split"], want)
	}
	mustContain(t, body, "moved", `id="pending-count"`, "hx-swap-oob")
	if got := s.proposal(id).Status; got != proposals.Applied {
		t.Errorf("status = %s", got)
	}
}

func TestApproveAllPassesNoSelectionAndPlainKindsMapMessageIDs(t *testing.T) {
	s := newSite(t)
	seen := map[string]string{}
	for _, kind := range []string{"rebalance_split", "deletion_rule", "rebalance_merge", "gmail_label_merge"} {
		s.recording(kind, seen)
	}
	first := s.splitProposal("split-1")
	s.post("/proposals/"+itoa(int(first))+"/approve", form("all", "1"), hx, "true")
	deletion := py.Dict{
		{Key: "rule", Value: py.Dict{{Key: "account", Value: "icloud"}, {Key: "when", Value: []any{}}}}, {Key: "reason", Value: "otp"},
		{Key: "older_than_days", Value: int64(7)}, {Key: "backlog", Value: int64(2)}, {Key: "senders", Value: []any{}},
		{Key: "subjects", Value: []any{}}, {Key: "messages", Value: []any{message(5), message(6)}},
	}
	second := s.propose("deletion_rule", "icloud", "Delete codes", deletion, "del-1")
	s.post("/proposals/"+itoa(int(second))+"/approve", form("msg-0", "6"), hx, "true")
	merge := py.Dict{{Key: "sources", Value: []any{}}, {Key: "into", Value: "A"}, {Key: "into_exists", Value: false}, {Key: "group", Value: group("A", 7)}}
	third := s.propose("rebalance_merge", "icloud", "Merge", merge, "merge-1")
	s.post("/proposals/"+itoa(int(third))+"/approve", form("name-0", "B", "msg-0", "7"), hx, "true")
	label := py.Dict{
		{Key: "from", Value: "x"}, {Key: "into", Value: "y"}, {Key: "from_count", Value: int64(1)}, {Key: "into_count", Value: int64(0)},
		{Key: "messages", Value: []any{message(8)}},
	}
	fourth := s.propose("gmail_label_merge", "gmail", "Labels", label, "label-1")
	s.post("/proposals/"+itoa(int(fourth))+"/approve", form(), hx, "true")
	want := map[string]string{
		"rebalance_split":   "None",
		"deletion_rule":     `{"message_ids": [6]}`,
		"rebalance_merge":   `{"groups": [{"name": "B", "message_ids": [7]}]}`,
		"gmail_label_merge": `{"message_ids": []}`,
	}
	for kind, selection := range want {
		if seen[kind] != selection {
			t.Errorf("%s selection = %s, want %s", kind, seen[kind], selection)
		}
	}
}

func TestSelectionErrorComesBackAsAToastAndStaysPending(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("split-1")
	s.registry.Executors["rebalance_split"] = func(context.Context, *proposals.Proposal, any) (py.Dict, error) {
		return nil, proposals.SelectionError("invalid folder name ''")
	}
	target := "/proposals/" + itoa(int(id)) + "/approve"
	body := status(t, s.post(target, form("name-0", "", "msg-0", "1"), hx, "true"), http.StatusOK)
	mustContain(t, body, "invalid folder name", "Not approved")
	if got := s.proposal(id).Status; got != proposals.Pending {
		t.Errorf("status = %s, want pending", got)
	}
	mustContain(t, status(t, s.post(target, form("msg-0", "x"), hx, "true"), http.StatusOK), "invalid message selection")
}

func TestFailedApplyShowsTheErrorAndKeepsTheCard(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("split-1")
	s.registry.Executors["rebalance_split"] = func(context.Context, *proposals.Proposal, any) (py.Dict, error) {
		return nil, errors.New("imap down")
	}
	body := status(t, s.post("/proposals/"+itoa(int(id))+"/approve", form("all", "1"), hx, "true"), http.StatusOK)
	mustContain(t, body, "It did not finish", "RuntimeError: imap down", `data-variant="destructive"`)
	if got := s.proposal(id).Status; got != proposals.Failed {
		t.Errorf("status = %s", got)
	}
}

func TestDismissAndUndoCardsUpdateTheBadge(t *testing.T) {
	s := newSite(t)
	keep, drop := s.splitProposal("a"), s.splitProposal("b")
	var undone []int64
	s.registry.Executors["rebalance_split"] = func(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
		_, err := proposals.RecordAction(ctx, s.db.Write, p.ID, py.Dict{{Key: "op", Value: "move"}}, py.Dict{{Key: "op", Value: "fake"}}, true, nil, 1.0)
		return py.Dict{{Key: "summary", Value: "ok"}}, err
	}
	s.registry.Undoers["fake"] = func(ctx context.Context, _ json.RawMessage) (py.Dict, error) {
		undone = append(undone, keep)
		return py.Dict{{Key: "restored", Value: true}}, nil
	}
	rec := s.post("/proposals/"+itoa(int(drop))+"/dismiss", form("forever", "1"), hx, "true")
	body := status(t, rec, http.StatusOK)
	mustContain(t, body, "Dismissed forever", `id="pending-count"`, "atrium will not propose this again.")
	if got := s.proposal(drop).Status; got != proposals.Suppressed {
		t.Errorf("status = %s", got)
	}
	s.post("/proposals/"+itoa(int(keep))+"/approve", form("all", "1"), hx, "true")
	if !s.proposal(keep).Undoable {
		t.Fatal("applied proposal is not undoable")
	}
	body = status(t, s.post("/proposals/"+itoa(int(keep))+"/undo", nil, hx, "true"), http.StatusOK)
	if len(undone) != 1 || !strings.Contains(body, "Undone") {
		t.Errorf("undone = %v, body = %s", undone, body)
	}
	mustContain(t, status(t, s.post("/proposals/"+itoa(int(drop))+"/dismiss", nil, hx, "true"), http.StatusOK), "Not dismissed")
}

func TestUndoThatDoesNotFinishReportsTheFirstFailure(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	s.registry.Executors["rebalance_split"] = func(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
		_, err := proposals.RecordAction(ctx, s.db.Write, p.ID, py.Dict{{Key: "op", Value: "move"}}, py.Dict{{Key: "op", Value: "fake"}}, true, nil, 1.0)
		return py.Dict{}, err
	}
	s.registry.Undoers["fake"] = func(context.Context, json.RawMessage) (py.Dict, error) { return nil, errors.New("folder changed") }
	s.post("/proposals/"+itoa(int(id))+"/approve", form("all", "1"), hx, "true")
	body := status(t, s.post("/proposals/"+itoa(int(id))+"/undo", nil, hx, "true"), http.StatusOK)
	mustContain(t, body, "Undo did not finish", "folder changed")
	mustContain(t, status(t, s.post("/proposals/"+itoa(int(id))+"/dismiss", nil, hx, "true"), http.StatusOK), "Not dismissed")
}

func TestProposalActionsOnAnUnknownIDAre404(t *testing.T) {
	s := newSite(t)
	for _, action := range []string{"approve", "dismiss", "undo"} {
		status(t, s.post("/proposals/999/"+action, nil, hx, "true"), http.StatusNotFound)
	}
	rec := s.post("/proposals/abc/approve", nil)
	body := status(t, rec, http.StatusUnprocessableEntity)
	if detail := jsonOf(t, body)["detail"].([]any)[0].(map[string]any); detail["type"] != "int_parsing" {
		t.Errorf("detail = %v", detail)
	}
}

func TestProposalsPageListsByStatusWithCountsAndBadge(t *testing.T) {
	s := newSite(t)
	first := s.splitProposal("a")
	s.splitProposal("other")
	status(t, s.post("/proposals/"+itoa(int(first))+"/dismiss", nil, hx, "true"), http.StatusOK)
	full := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, full, "Split Shop")
	if !strings.Contains(compact(full), ">1<span") {
		t.Error("sidebar badge does not show 1 pending")
	}
	mustContain(t, status(t, s.get("/proposals?status=dismissed", hx, "true"), http.StatusOK), "Split Shop", "1 dismissed proposal")
	status(t, s.get("/proposals?status=all", hx, "true"), http.StatusOK)
	bad := status(t, s.get("/proposals?status=nonsense"), http.StatusUnprocessableEntity)
	mustContain(t, bad, "Unknown status.", "Clear filters")
	badKind := status(t, s.get("/proposals?kind=nonsense", hx, "true"), http.StatusUnprocessableEntity)
	mustContain(t, badKind, "Unknown kind.", `id="toaster"`)
	mustContain(t, status(t, s.get("/activity"), http.StatusOK), `id="pending-count"`)
}

func TestEveryOfferedProposalKindIsAccepted(t *testing.T) {
	s := newSite(t)
	page := status(t, s.get("/proposals"), http.StatusOK)
	offered := optionValues(between(page, `<select id="p-kind"`, "</select>"))[1:]
	if len(offered) != 10 {
		t.Fatalf("offered %d kinds, want 10", len(offered))
	}
	for _, kind := range offered {
		status(t, s.get("/proposals?kind="+kind, hx, "true"), http.StatusOK)
	}
}

func TestFilterListIsAFragmentWithItsOwnTitle(t *testing.T) {
	s := newSite(t)
	s.splitProposal("a")
	body := status(t, s.get("/proposals?status=pending&kind=rebalance_split", hx, "true"), http.StatusOK)
	mustContain(t, body, "<title>Proposals · atrium</title>", "1 pending proposal · Split folder")
	mustNotContain(t, body, "<html", "Approving runs it now")
	restore := status(t, s.get("/proposals", hx, "true", "HX-History-Restore-Request", "true"), http.StatusOK)
	mustContain(t, restore, "<html")
}

func TestDeletionProposalWithAnAgeConditionRenders(t *testing.T) {
	s := newSite(t)
	rule := py.Dict{{Key: "account", Value: "icloud"}, {Key: "when", Value: []any{
		py.Dict{{Key: "field", Value: "subject"}, {Key: "op", Value: "re"}, {Key: "value", Value: "code"}},
		py.Dict{{Key: "field", Value: "age_days"}, {Key: "op", Value: "gte"}, {Key: "value", Value: int64(7)}},
	}}}
	preview := py.Dict{
		{Key: "rule", Value: rule}, {Key: "reason", Value: "otp"}, {Key: "older_than_days", Value: int64(7)}, {Key: "backlog", Value: int64(1)},
		{Key: "senders", Value: []any{}}, {Key: "subjects", Value: []any{}}, {Key: "messages", Value: []any{message(5)}},
	}
	s.propose("deletion_rule", "icloud", "Delete codes", preview, "del-age")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, "Age in days", "is at least", "data-approve-picked", "Save rule only",
		`data-confirm-none="Save the rule without moving any messages to Trash?"`,
		`data-confirm="Move all matching messages to Trash now and save the rule?"`, "One-time codes are useless once they expire.",
		"The rule also deletes new matching mail from now on.")
	mustNotContain(t, page, "hx-confirm=", "is over")
}

func TestDeletionRuleWithoutAPickerSavesTheRuleOnly(t *testing.T) {
	s := newSite(t)
	preview := py.Dict{
		{Key: "rule", Value: py.Dict{{Key: "when", Value: []any{py.Dict{{Key: "field", Value: "from_domain"}, {Key: "op", Value: "eq"}, {Key: "value", Value: "x.test"}}}}}},
		{Key: "reason", Value: "operator_deleted"}, {Key: "backlog", Value: int64(0)}, {Key: "senders", Value: []any{}},
		{Key: "subjects", Value: []any{}}, {Key: "messages", Value: []any{}},
	}
	s.propose("deletion_rule", "icloud", "Delete", preview, "del-none")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, `data-confirm="Save the rule? Nothing moves to Trash now."`, "You have deleted mail like this before.", "existing messages match")
	mustNotContain(t, page, "data-approve-picked", "Approve all", "data-selection", "hx-confirm=")
}

func TestPickerCardFromARealRowShowsTheMessageChoices(t *testing.T) {
	s := newSite(t)
	s.splitProposal("a")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, "data-selection", `name="msg-0"`, `name="msg-1"`, `value="4"`, `name="name-0"`, `value="Shop/Orders"`,
		"Approve all", `data-picked-total>4</span>`, `aria-label="Read Subject 1"`, `hx-get="/messages/1"`, "Choose exactly which messages move",
		`<span data-picked>2</span> of 2 messages`)
}

func TestGroupWithoutMessagesShowsItsCount(t *testing.T) {
	s := newSite(t)
	empty := py.Dict{{Key: "name", Value: "Shop/Empty"}, {Key: "count", Value: int64(3)}, {Key: "messages", Value: []any{}}}
	preview := py.Dict{{Key: "folder", Value: "INBOX/Shop"}, {Key: "groups", Value: []any{group("Shop/Orders", 1), empty}}}
	s.propose("rebalance_split", "icloud", "Split", preview, "mixed")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, "3 messages", `<span data-picked>1</span> of 1 messages`, "Approve all")
}

func TestRebalanceMergeIntoAnExistingFolderIsNotEditable(t *testing.T) {
	s := newSite(t)
	merge := func(exists bool, fingerprint string) string {
		preview := py.Dict{
			{Key: "sources", Value: []any{py.Dict{{Key: "folder", Value: "Old"}, {Key: "count", Value: int64(2)}}}},
			{Key: "into", Value: "Shop|Orders"}, {Key: "into_exists", Value: exists}, {Key: "group", Value: group("Shop|Orders", 1, 2)},
		}
		s.propose("rebalance_merge", "icloud", "Merge "+fingerprint, preview, fingerprint)
		return status(t, s.get("/proposals?kind=rebalance_merge", hx, "true"), http.StatusOK)
	}
	existing := merge(true, "exists")
	mustContain(t, existing, "existing folder", "Shop · Orders")
	mustNotContain(t, existing, `name="name-0"`)
	fresh := merge(false, "fresh")
	mustContain(t, fresh, `name="name-0"`, "new folder")
}

func TestCalendarAndNotionCardsToleratePartialPreviews(t *testing.T) {
	s := newSite(t)
	s.propose("calendar_event", "", "Lunch", py.Dict{
		{Key: "calendar", Value: "google"}, {Key: "all_day", Value: true}, {Key: "start", Value: "2026-11-01"}, {Key: "end", Value: "2026-11-02"},
	}, "cal")
	s.propose("notion_link", "", "Link", py.Dict{
		{Key: "a", Value: py.Dict{{Key: "id", Value: "p-a"}, {Key: "title", Value: "A"}, {Key: "url", Value: "https://notion.so/a"}}},
		{Key: "b", Value: py.Dict{{Key: "id", Value: "p-b"}, {Key: "title", Value: nil}, {Key: "url", Value: "https://notion.so/b"}}},
	}, "link")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, "Google", "<dt class=\"text-muted-foreground\">Date</dt>", "2026-11-01", "Nobody is invited", "Untitled",
		`href="/notion/p-a"`, `aria-label="Open Untitled in Notion"`)
	mustNotContain(t, page, "Where", "Notes", "<blockquote")
}

func TestWikiPlanKeepsTheOrderOfItsCounts(t *testing.T) {
	s := newSite(t)
	preview := py.Dict{
		{Key: "counts", Value: py.Dict{{Key: "keep", Value: int64(3)}, {Key: "drop", Value: int64(1)}, {Key: "convert", Value: int64(2)}}},
		{Key: "hubs", Value: []any{py.Dict{{Key: "title", Value: "Home"}}}}, {Key: "databases", Value: []any{}},
		{Key: "items", Value: []any{py.Dict{{Key: "id", Value: "n1"}, {Key: "decision", Value: "drop"}, {Key: "title", Value: nil}, {Key: "reason", Value: "stale"}}}},
	}
	s.propose("notion_wiki_plan", "", "Wiki", preview, "wiki")
	page := status(t, s.get("/proposals?kind=notion_wiki_plan", hx, "true"), http.StatusOK)
	flat := text(page)
	keep, drop, convert := strings.Index(flat, "3 keep"), strings.Index(flat, "1 drop"), strings.Index(flat, "2 convert")
	if keep < 0 || keep > drop || drop > convert {
		t.Errorf("counts are not in plan order: %s", flat)
	}
	mustContain(t, page, `data-variant="destructive"`, "Every row and page (1)", `href="/notion/n1"`, "Untitled")
}

func TestFailedAndAppliedFootersByKind(t *testing.T) {
	s := newSite(t)
	failed := s.splitProposal("failed")
	s.exec("UPDATE proposals SET status = 'failed', decided_ts = 1000, result = ? WHERE id = ?", `{"error": "boom <b>"}`, failed)
	wiki := s.propose("notion_wiki_plan", "", "Wiki", py.Dict{
		{Key: "counts", Value: py.Dict{}}, {Key: "hubs", Value: []any{}}, {Key: "databases", Value: []any{}}, {Key: "items", Value: []any{}},
	}, "wiki")
	s.exec("UPDATE proposals SET status = 'failed', decided_ts = 1000 WHERE id = ?", wiki)
	applied := s.propose("reminder", "", "Remind", py.Dict{{Key: "text", Value: "call"}, {Key: "due", Value: "2026-11-01"}, {Key: "remind_at", Value: "09:00"}}, "rem")
	s.exec("UPDATE proposals SET status = 'applied', applied_ts = 1000, decided_ts = 1000 WHERE id = ?", applied)
	page := status(t, s.get("/proposals?status=all", hx, "true"), http.StatusOK)
	mustContain(t, page, "boom &lt;b&gt;", "Tried", "Resume", "Skips the steps the earlier attempt finished", ">Applied<")
	if strings.Count(page, "Resume") != 1 {
		t.Errorf("Resume appears %d times, want only the wiki plan", strings.Count(page, "Resume"))
	}
	mustNotContain(t, page, "Undo what it did", "hx-confirm=")
}

func TestUndoableAppliedWithoutSummaryShowsApplied(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	s.registry.Executors["rebalance_split"] = func(ctx context.Context, p *proposals.Proposal, _ any) (py.Dict, error) {
		_, err := proposals.RecordAction(ctx, s.db.Write, p.ID, py.Dict{{Key: "op", Value: "move"}}, py.Dict{{Key: "op", Value: "fake"}}, true, nil, 1.0)
		return py.Dict{}, err
	}
	body := status(t, s.post("/proposals/"+itoa(int(id))+"/approve", form("all", "1"), hx, "true"), http.StatusOK)
	mustContain(t, body, "Applied", `hx-confirm="Undo this proposal and put everything back?"`, `hx-post="/proposals/`+itoa(int(id))+`/undo"`)
}

func TestShadowModeNotesAndDeletionRuleCopy(t *testing.T) {
	s := newSite(t, func(env map[string]string) { env["ATRIUM_MODE"] = "shadow" })
	preview := py.Dict{
		{Key: "rule", Value: py.Dict{{Key: "when", Value: []any{}}}}, {Key: "reason", Value: "otp"}, {Key: "backlog", Value: int64(1)},
		{Key: "senders", Value: []any{}}, {Key: "subjects", Value: []any{}}, {Key: "messages", Value: []any{message(1)}},
	}
	s.propose("deletion_rule", "icloud", "Delete", preview, "d")
	page := status(t, s.get("/proposals"), http.StatusOK)
	mustContain(t, page, ", even though nothing is applied automatically in shadow mode", "only once routing is live", "existing message matches")
}

func TestEmptyStates(t *testing.T) {
	s := newSite(t)
	mustContain(t, status(t, s.get("/proposals"), http.StatusOK), "Nothing to review", "0 pending proposals")
	mustContain(t, status(t, s.get("/proposals?status=failed"), http.StatusOK), "No proposals match", "Choose another status or kind.")
}

func TestUndoOfAProposalThatIsNotAppliedReportsNotUndone(t *testing.T) {
	s := newSite(t)
	id := s.splitProposal("a")
	body := status(t, s.post("/proposals/"+itoa(int(id))+"/undo", nil, hx, "true"), http.StatusOK)
	mustContain(t, body, "Not undone", "is not applied")
}
