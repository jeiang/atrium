package reviewpages

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) proposalLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	ctx := r.Context()
	found, err := proposals.Get(ctx, s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	thread, known, err := proposals.ThreadOf(ctx, s.deps.DB.Read, id)
	check(err)
	target := fmt.Sprintf("/review#proposal-%d", id)
	if known {
		target = fmt.Sprintf("/chat/%d#proposal-%d", thread, id)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func rowView(r *http.Request) bool {
	if web.QueryValue(r.URL.Query(), "view") == "row" {
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	view, _ := web.Value(web.FormValues(r), "view")
	return view == "row"
}

func (s *service) card(w http.ResponseWriter, r *http.Request, id int64, notice *tpl.Toast) {
	found, err := proposals.Get(r.Context(), s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown proposal")
		return
	}
	if r.Method == http.MethodGet && web.QueryValue(r.URL.Query(), "poll") != "" && found.Status == proposals.Pending {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	kind, _ := s.registry.Kind(found.Kind)
	view := proposalView(found, kind)
	if rowView(r) {
		s.fragment(w, r, http.StatusOK, "_proposal_row.gohtml", proposalCardData{Proposal: view, PendingOOB: true}, notice)
		return
	}
	s.fragment(w, r, http.StatusOK, "_proposal.gohtml", proposalCardData{Proposal: view, PendingOOB: true}, notice)
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

func (s *service) proposalCard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	s.card(w, r, id, nil)
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
	err := s.pipeline.Dismiss(detached(r), id, status, proposals.Resolver{Actor: "operator", Source: "ui"})
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
	outcome, err := s.pipeline.Undo(detached(r), id, proposals.Resolver{Actor: "operator", Source: "ui"})
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
