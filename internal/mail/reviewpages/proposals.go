package reviewpages

import (
	"errors"
	"fmt"
	"strconv"

	"net/http"
	"net/url"
	"slices"

	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const allStatuses = "all"

var proposalStatuses = []string{
	proposals.Pending, proposals.Applied, proposals.Failed, proposals.Dismissed, proposals.Suppressed, allStatuses,
}

func (s *service) kindLabel(name string) string {
	kind, _ := s.registry.Kind(name)
	return kind.Label
}

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
	if kind != "" && !s.registry.Known(kind) {
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
	views := proposalViews(list, s.registry)
	data := proposalListData{Proposals: views, Status: status, Kind: kind, KindLabel: s.kindLabel(kind), Counts: counts}
	if web.IsPartial(r) {
		data.Partial = true
		s.fragment(w, r, http.StatusOK, "_proposal_list.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "proposals", "proposals.gohtml", data, nil)
}

func (s *service) card(w http.ResponseWriter, r *http.Request, id int64, notice *tpl.Toast) {
	found, err := proposals.Get(r.Context(), s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	kind, _ := s.registry.Kind(found.Kind)
	view := []ProposalView{proposalView(found, kind)}
	s.fragment(w, r, http.StatusOK, "_proposal.gohtml", proposalCardData{Proposal: view[0], PendingOOB: true}, notice)
}

func picked(form url.Values, index int, messages any) ([]any, error) {
	if !truthy(messages) {
		return nil, nil
	}
	ids := []any{}
	for _, value := range form[fmt.Sprintf("msg-%d", index)] {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, proposals.SelectionError("invalid message selection")
		}
		ids = append(ids, n)
	}
	return ids, nil
}

func previewGet(preview any, key string) any {
	if d, ok := preview.(map[string]any); ok {
		return d[key]
	}
	return nil
}

func selectionOf(p *proposals.Proposal, kind proposals.Kind, form url.Values) (map[string]any, error) {
	switch {
	case kind.Groups || kind.Group:
		var groups []any
		if kind.Groups {
			groups, _ = previewGet(p.Preview, "groups").([]any)
		} else {
			groups = []any{previewGet(p.Preview, "group")}
		}
		out := make([]any, len(groups))
		for i, g := range groups {
			group, _ := g.(map[string]any)
			var name any
			if v, ok := web.Value(form, fmt.Sprintf("name-%d", i)); ok {
				name = v
			}
			messages := group["messages"]
			ids, err := picked(form, i, messages)
			if err != nil {
				return nil, err
			}
			var chosen any
			if ids != nil {
				chosen = ids
			}
			id := group["name"]
			out[i] = map[string]any{"id": id, "name": name, "message_ids": chosen}
		}
		return map[string]any{"groups": out}, nil
	case kind.Messages:
		ids, err := picked(form, 0, previewGet(p.Preview, "messages"))
		if err != nil {
			return nil, err
		}
		var chosen any
		if ids != nil {
			chosen = ids
		}
		return map[string]any{"message_ids": chosen}, nil
	}
	return nil, nil
}

func (s *service) proposalApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	ctx := detached(r)
	found, err := proposals.Get(ctx, s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	form := web.FormValues(r)
	var selection any
	var done *proposals.Proposal
	if all, _ := web.Value(form, "all"); all == "" {
		var picked map[string]any
		kind, _ := s.registry.Kind(found.Kind)
		if picked, err = selectionOf(found, kind, form); picked != nil {
			selection = picked
		}
	}
	if err == nil {
		done, err = s.pipeline.ApproveAndApply(ctx, id, selection, proposals.Resolver{Actor: "operator", Source: "ui"})
	}
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		s.card(w, r, id, toast("error", "Not approved", err.Error()))
		return
	}
	check(err)
	if done.Status == proposals.Applied {
		s.card(w, r, id, toast("success", "Applied", resultString(done.Result, "summary")))
		return
	}
	s.card(w, r, id, toast("error", "It did not finish", resultString(done.Result, "error")))
}

func (s *service) proposalDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	forever, _ := web.Value(web.FormValues(r), "forever")
	status := proposals.Dismissed
	if forever != "" {
		status = proposals.Suppressed
	}
	err := proposals.Dismiss(detached(r), s.deps.DB, id, status, proposals.Resolver{Actor: "operator", Source: "ui"}, s.now())
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
		if okOutcome := o["ok"]; okOutcome != true {
			text := o["error"]
			failed, _ = text.(string)
			break
		}
	}
	s.card(w, r, id, toast("error", "Undo did not finish", failed))
}

func resolvedToast(outcome string) *tpl.Toast {
	const untouched = " atrium did not touch the mailbox."
	switch outcome {
	case proposals.OutcomeUndone:
		return toast("success", "Undo recorded as done", "You checked that the messages are back."+untouched)
	case proposals.OutcomeNotUndone:
		return toast("success", "Undo recorded as not done", "You checked that the messages were not moved back, so the undo can be tried again."+untouched)
	case proposals.OutcomeApplied:
		return toast("success", "Write recorded as done", "You checked that the write happened."+untouched)
	case proposals.OutcomeNotApplied:
		return toast("success", "Write recorded as not done", "You checked that the write did not happen, so the action can be tried again."+untouched)
	}
	return toast("success", "Marked as checked", "The blocked write is recorded as checked by you."+untouched)
}

func (s *service) proposalResolve(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	_, err := s.pipeline.Resolve(detached(r), id, outcome, proposals.Resolver{Actor: "operator", Source: "ui"})
	var refused proposals.ProposalError
	if errors.As(err, &refused) {
		s.card(w, r, id, toast("error", "Not settled", err.Error()))
		return
	}
	check(err)
	s.card(w, r, id, resolvedToast(outcome))
}

func resultString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		s, _ := x[key].(string)
		return s
	}
	return ""
}
