package reviewpages

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var statusFilters = []drafts.Status{drafts.Draft, drafts.Unknown, drafts.Sent, drafts.Discarded}

const draftLimit = 100

func statusNames() []string {
	out := make([]string, len(statusFilters))
	for i, status := range statusFilters {
		out[i] = string(status)
	}
	return out
}

func (s *service) draftViews(r *http.Request, rows []drafts.Row) []DraftView {
	unexpected, err := s.drafts.UnexpectedRecipientsBatch(r.Context(), rows)
	check(err)
	out := make([]DraftView, len(rows))
	for i, row := range rows {
		out[i] = draftView(row, unexpected[i])
	}
	return out
}

func (s *service) showDrafts(w http.ResponseWriter, r *http.Request, status drafts.Status, code int, notice *tpl.Toast) {
	rows, err := s.drafts.List(r.Context(), status, draftLimit)
	check(err)
	data := draftsPage{Drafts: s.draftViews(r, rows), Status: string(status), Statuses: statusNames(), Configured: accountConfigured(r.Context())}
	s.page(w, r, code, "drafts", "drafts.gohtml", data, notice)
}

func (s *service) draftCard(w http.ResponseWriter, r *http.Request, id int64, code int, notice *tpl.Toast) {
	row, err := s.drafts.Get(r.Context(), id)
	check(err)
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "unknown draft")
		return
	}
	s.renderDraftCard(w, r, s.draftViews(r, []drafts.Row{*row})[0], code, notice)
}

func (s *service) renderDraftCard(w http.ResponseWriter, r *http.Request, view DraftView, code int, notice *tpl.Toast) {
	data := draftCardData{D: view, Configured: accountConfigured(r.Context()), Partial: true}
	s.fragment(w, r, code, "_draft.gohtml", data, notice)
}

func (s *service) reply(w http.ResponseWriter, r *http.Request, id int64, status drafts.Status, code int, notice *tpl.Toast) {
	if web.IsPartial(r) {
		s.draftCard(w, r, id, code, notice)
		return
	}
	s.showDrafts(w, r, status, code, notice)
}

func (s *service) draftsPage(w http.ResponseWriter, r *http.Request) {
	status := drafts.Draft
	if r.URL.Query().Has("status") {
		status = drafts.Status(web.QueryValue(r.URL.Query(), "status"))
	}
	if status != "" && !slices.Contains(statusFilters, status) && status != drafts.Sending {
		s.fail(w, r, http.StatusUnprocessableEntity, "unknown status")
		return
	}
	s.showDrafts(w, r, status, http.StatusOK, nil)
}

func (s *service) draftPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	row, err := s.drafts.Get(r.Context(), id)
	check(err)
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "unknown draft")
		return
	}
	view := s.draftViews(r, []drafts.Row{*row})[0]
	configured := accountConfigured(r.Context())
	if web.IsPartial(r) {
		s.fragment(w, r, http.StatusOK, "_draft_preview.gohtml", draftCardData{D: view, Configured: configured}, nil)
		return
	}
	s.page(w, r, http.StatusOK, "drafts", "drafts.gohtml",
		draftsPage{Drafts: []DraftView{view}, Status: view.Status, Statuses: statusNames(), Configured: configured}, nil)
}

func (s *service) draftCardPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	s.draftCard(w, r, id, http.StatusOK, nil)
}

func (s *service) draftUpdate(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "draft_id")
	form := web.FormValues(r)
	account, found := web.Value(form, "account")
	if !found || account == "" {
		errs = append(errs, web.Missing("body", "account"))
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	changes := map[string]any{"account": account}
	for _, name := range []string{"to", "cc", "subject", "body"} {
		value, _ := web.Value(form, name)
		changes[name] = value
	}
	_, err := s.drafts.Update(detached(r), id, changes)
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		row, gerr := s.drafts.Get(detached(r), id)
		if gerr == nil && row != nil {
			submitted := *row
			submitted.Account = account
			if v, ok := changes["to"].(string); ok {
				submitted.ToAddrs = v
			}
			if v, ok := changes["cc"].(string); ok {
				submitted.CcAddrs = v
			}
			if v, ok := changes["subject"].(string); ok {
				submitted.Subject = v
			}
			if v, ok := changes["body"].(string); ok {
				submitted.Body = v
			}
			unexpected, _ := s.drafts.UnexpectedRecipients(detached(r), submitted)
			view := draftView(submitted, unexpected)
			if web.IsPartial(r) {
				s.renderDraftCard(w, r, view, http.StatusUnprocessableEntity, toast("error", "Not saved", err.Error()))
			} else {
				s.page(w, r, http.StatusUnprocessableEntity, "drafts", "drafts.gohtml",
					draftsPage{Drafts: []DraftView{view}, Status: view.Status, Statuses: statusNames(), Configured: accountConfigured(r.Context())},
					toast("error", "Not saved", err.Error()))
			}
			return
		}
		s.reply(w, r, id, drafts.Draft, http.StatusUnprocessableEntity, toast("error", "Not saved", err.Error()))
		return
	}
	var stale drafts.Stale
	if errors.As(err, &stale) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not saved", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Draft, http.StatusOK, toast("success", "Saved", "Draft updated."))
}

func (s *service) draftDiscard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	err := s.drafts.Discard(detached(r), id)
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not discarded", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Draft, http.StatusOK, toast("success", "Discarded", "Draft discarded."))
}

func (s *service) draftSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	revision, _ := web.Value(web.FormValues(r), "revision")
	var result drafts.Result
	var err error
	if rev, perr := strconv.ParseInt(revision, 10, 64); perr == nil {
		result, err = s.drafts.Send(detached(r), id, rev)
	} else {
		err = drafts.Error("draft changed, review again")
	}
	var unknown drafts.OutcomeUnknown
	if errors.As(err, &unknown) {
		s.reply(w, r, id, drafts.Unknown, http.StatusConflict, toast("error", "Send outcome unknown", err.Error()))
		return
	}
	var refused drafts.Error
	if errors.As(err, &refused) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not sent", err.Error()))
		return
	}
	if err != nil {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not sent", err.Error()))
		return
	}
	detail := result.Filed.String()
	if result.FiledError != "" {
		detail += ": " + result.FiledError
	}
	if len(result.Refused) > 0 {
		addrs := make([]string, len(result.Refused))
		for i, refusal := range result.Refused {
			addrs[i] = refusal.Addr
		}
		detail += "; refused: " + strings.Join(addrs, ", ")
	}
	s.reply(w, r, id, drafts.Draft, http.StatusOK, toast("success", "Sent", detail))
}

func (s *service) draftResolve(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	var verdict drafts.Status
	switch outcome {
	case "sent":
		verdict = drafts.Sent
	case "draft":
		verdict = drafts.Draft
	default:
		s.fail(w, r, http.StatusUnprocessableEntity, "outcome must be sent or draft")
		return
	}
	err := s.drafts.ResolveUnknown(detached(r), id, verdict, ledger.Resolver{Actor: "operator", Source: "ui"})
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		s.reply(w, r, id, drafts.Unknown, http.StatusConflict, toast("error", "Not updated", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Unknown, http.StatusOK, toast("success", "Updated", "Draft marked "+string(verdict)+"."))
}
