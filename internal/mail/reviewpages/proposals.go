package reviewpages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/proposals"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func (s *service) proposalLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	found, err := s.review.Proposal(r.Context(), reviewapp.ProposalInput{ID: id})
	if err != nil {
		s.failRead(w, r, err, "unknown proposal")
		return
	}
	target := fmt.Sprintf("/review#proposal-%d", id)
	if found.Data.LinkThread != nil {
		target = fmt.Sprintf("/chat/%d#proposal-%d", *found.Data.LinkThread, id)
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
	found, err := s.review.Proposal(r.Context(), reviewapp.ProposalInput{ID: id})
	if err != nil {
		s.failRead(w, r, err, "unknown proposal")
		return
	}
	if r.Method == http.MethodGet && web.QueryValue(r.URL.Query(), "poll") != "" && found.Data.Status == proposals.Pending {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	view := proposalView(found.Data)
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
	in := reviewapp.ApproveInput{ProposalID: id}
	if all, _ := web.Value(form, "all"); all == "" {
		kind, _ := s.registry.Kind(found.Kind)
		picked, err := selectionOf(found, kind, form)
		if err != nil {
			s.card(w, r, id, toast("error", "Not approved", err.Error()))
			return
		}
		in.Selection = picked
	}
	done, err := s.review.Approve(ctx, in)
	refused, answered := s.decided(w, r, err, "unknown proposal")
	if answered {
		return
	}
	if refused != nil {
		if done.Data.Proposal != nil {
			s.card(w, r, id, toast("error", "It did not finish", stoppedPartway(refused, done.Data.Proposal)))
			return
		}
		s.card(w, r, id, toast("error", "Not approved", refused.Error()))
		return
	}
	if done.Data.Applied {
		s.card(w, r, id, toast("success", "Applied", resultString(done.Data.Proposal.Result, "summary")))
		return
	}
	s.card(w, r, id, toast("error", "It did not finish", resultString(done.Data.Proposal.Result, "error")))
}

func (s *service) decided(w http.ResponseWriter, r *http.Request, err error, missing string) (error, bool) {
	if err == nil {
		return nil, false
	}
	var e *application.Error
	if !errors.As(err, &e) {
		panic(err)
	}
	switch e.Code {
	case application.Invalid, application.Refused, application.Conflict:
		return e, false
	}
	s.failRead(w, r, err, missing)
	return nil, true
}

func (s *service) phone(ctx context.Context, token string, now float64) (*proposals.Proposal, error) {
	done, err := s.review.Phone(ctx, reviewapp.PhoneInput{Token: token, Now: now})
	return done.Data.Proposal, err
}
func (s *service) proposalDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "proposal_id")
	if !ok {
		return
	}
	forever, _ := web.Value(web.FormValues(r), "forever")
	_, err := s.review.Dismiss(detached(r), reviewapp.DismissInput{ProposalID: id, Suppress: forever != ""})
	refused, answered := s.decided(w, r, err, "unknown proposal")
	if answered {
		return
	}
	if refused != nil {
		s.card(w, r, id, toast("error", "Not dismissed", refused.Error()))
		return
	}
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
	done, err := s.review.Undo(detached(r), reviewapp.UndoInput{ProposalID: id})
	outcome := done.Data.Outcome
	refused, answered := s.decided(w, r, err, "unknown proposal")
	if answered {
		return
	}
	if refused != nil {
		if len(outcome.Outcomes) > 0 || outcome.SyncError != nil {
			s.card(w, r, id, toast("error", "Undo stopped partway", undoStoppedPartway(refused, outcome)))
			return
		}
		s.card(w, r, id, toast("error", "Not undone", refused.Error()))
		return
	}
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
func stoppedPartway(err error, done *proposals.Proposal) string {
	if done.Undoable {
		return err.Error() + ". Undo what it already did from this card."
	}
	return err.Error() + "."
}

func undoStoppedPartway(err error, outcome proposals.UndoResult) string {
	put := 0
	for _, o := range outcome.Outcomes {
		if o["ok"] == true {
			put++
		}
	}
	text := fmt.Sprintf("%s. Writes put back before it stopped: %d.", err.Error(), put)
	if outcome.SyncError != nil {
		text += " The mailbox index did not refresh: " + *outcome.SyncError
	}
	return text
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
	_, err := s.review.Resolve(detached(r), reviewapp.ResolveInput{ProposalID: id, Outcome: outcome})
	refused, answered := s.decided(w, r, err, "unknown proposal")
	if answered {
		return
	}
	if refused != nil {
		s.card(w, r, id, toast("error", "Not settled", refused.Error()))
		return
	}
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
