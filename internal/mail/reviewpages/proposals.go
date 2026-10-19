package reviewpages

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"

	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const allStatuses = "all"

var proposalStatuses = []string{
	proposals.Pending, proposals.Applied, proposals.Failed, proposals.Dismissed, proposals.Suppressed, allStatuses,
}

// proposalKinds is every kind the Kind filter offers.
var proposalKinds = []string{
	executors.RebalanceSplit, executors.RebalanceMerge, executors.InboxBacklog, executors.DeletionRule, executors.GmailLabelMerge,
	"notion_link", "notion_edit", "calendar_event", "reminder", kindWikiPlan,
}

var (
	groupedKinds = []string{executors.RebalanceSplit, executors.InboxBacklog}
	splitKinds   = []string{executors.RebalanceSplit, executors.RebalanceMerge, executors.InboxBacklog}
	messageKinds = []string{executors.DeletionRule, executors.GmailLabelMerge}
)

func (s *service) proposalsPage(w http.ResponseWriter, r *http.Request) {
	status := web.QueryValue(r.URL.Query(), "status")
	if status == "" {
		status = proposals.Pending
	}
	kind := web.QueryValue(r.URL.Query(), "kind")
	if !slices.Contains(proposalStatuses, status) {
		s.fail(w, r, http.StatusUnprocessableEntity, "unknown status")
		return
	}
	if kind != "" && !slices.Contains(proposalKinds, kind) {
		s.fail(w, r, http.StatusUnprocessableEntity, "unknown kind")
		return
	}
	filter := status
	if status == allStatuses {
		filter = ""
	}
	ctx := r.Context()
	list, err := proposals.List(ctx, s.deps.DB.Read, filter, kind)
	check(err)
	counts, err := proposals.CountsByStatus(ctx, s.deps.DB.Read)
	check(err)
	data := proposalListData{Proposals: proposalViews(list), Status: status, Kind: kind, Counts: counts}
	if web.IsPartial(r) {
		data.Partial = true
		s.fragment(w, r, http.StatusOK, "_proposal_list.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "proposals", "proposals.gohtml", data, nil)
}

// card is the proposal's card with notice and the out-of-band pending count; a proposal that is not there is 404.
func (s *service) card(w http.ResponseWriter, r *http.Request, id int64, notice *tpl.Toast) {
	found, err := proposals.Get(r.Context(), s.deps.DB.Write, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	s.fragment(w, r, http.StatusOK, "_proposal.gohtml", proposalCardData{Proposal: proposalView(found), PendingOOB: true}, notice)
}

// picked is the message ids ticked for group index: nil (every message) when the group has none to choose from.
func picked(form url.Values, index int, messages any) ([]any, error) {
	if !py.Truthy(messages) {
		return nil, nil
	}
	ids := []any{}
	for _, value := range form[fmt.Sprintf("msg-%d", index)] {
		n, err := py.IntString(value)
		if err != nil {
			return nil, proposals.SelectionError("invalid message selection")
		}
		if n.IsInt64() {
			ids = append(ids, n.Int64())
		} else {
			ids = append(ids, new(big.Int).Set(n))
		}
	}
	return ids, nil
}

func previewGet(preview any, key string) any {
	if d, ok := preview.(py.Dict); ok {
		v, _ := d.Get(key)
		return v
	}
	return nil
}

// selectionOf is selection_of: what the card's form picked, for the kinds that offer a choice.
func selectionOf(p *proposals.Proposal, form url.Values) (py.Dict, error) {
	switch {
	case slices.Contains(splitKinds, p.Kind):
		var groups []any
		if slices.Contains(groupedKinds, p.Kind) {
			groups, _ = previewGet(p.Preview, "groups").([]any)
		} else {
			groups = []any{previewGet(p.Preview, "group")}
		}
		out := make([]any, len(groups))
		for i, g := range groups {
			group, _ := g.(py.Dict)
			var name any
			if v, ok := web.FormLast(form, fmt.Sprintf("name-%d", i)); ok {
				name = v
			}
			messages, _ := group.Get("messages")
			ids, err := picked(form, i, messages)
			if err != nil {
				return nil, err
			}
			var chosen any
			if ids != nil {
				chosen = ids
			}
			out[i] = py.Dict{{Key: "name", Value: name}, {Key: "message_ids", Value: chosen}}
		}
		return py.Dict{{Key: "groups", Value: out}}, nil
	case slices.Contains(messageKinds, p.Kind):
		ids, err := picked(form, 0, previewGet(p.Preview, "messages"))
		if err != nil {
			return nil, err
		}
		var chosen any
		if ids != nil {
			chosen = ids
		}
		return py.Dict{{Key: "message_ids", Value: chosen}}, nil
	}
	return nil, nil
}

func (s *service) proposalApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	ctx := detached(r)
	found, err := proposals.Get(ctx, s.deps.DB.Write, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	form := web.FormValues(r)
	var selection any
	var done *proposals.Proposal
	if all, _ := web.FormLast(form, "all"); all == "" {
		var picked py.Dict
		if picked, err = selectionOf(found, form); picked != nil {
			selection = picked
		}
	}
	if err == nil {
		done, err = s.pipeline.ApproveAndApply(ctx, id, selection)
	}
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		s.card(w, r, id, toast("error", "Not approved", err.Error()))
		return
	}
	check(err)
	result, _ := plainJSON(done.Result).(map[string]any)
	if done.Status == proposals.Applied {
		summary, _ := result["summary"].(string)
		s.card(w, r, id, toast("success", "Applied", summary))
		return
	}
	failure, _ := result["error"].(string)
	s.card(w, r, id, toast("error", "It did not finish", failure))
}

func (s *service) proposalDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	forever, _ := web.FormField(web.FormValues(r), "forever")
	err := proposals.Dismiss(detached(r), s.deps.DB.Write, id, forever != "", s.now())
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		s.card(w, r, id, toast("error", "Not dismissed", err.Error()))
		return
	}
	check(err)
	if forever != "" {
		s.card(w, r, id, toast("success", "Dismissed forever"))
		return
	}
	s.card(w, r, id, toast("success", "Dismissed"))
}

func (s *service) proposalUndo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	outcome, err := s.pipeline.Undo(detached(r), id)
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		s.card(w, r, id, toast("error", "Not undone", err.Error()))
		return
	}
	check(err)
	if outcome.Undone {
		s.card(w, r, id, toast("success", "Undone"))
		return
	}
	failed := ""
	for _, o := range outcome.Outcomes {
		if okOutcome, _ := o.Get("ok"); okOutcome != true {
			text, _ := o.Get("error")
			failed, _ = text.(string)
			break
		}
	}
	s.card(w, r, id, toast("error", "Undo did not finish", failed))
}
