package reviewpages

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	reviewapp "github.com/jeiang/atrium/internal/review/application"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var statusFilters = []drafts.Status{drafts.Draft, drafts.Unknown, drafts.Sent, drafts.Discarded}

func statusNames() []string {
	out := make([]string, len(statusFilters))
	for i, status := range statusFilters {
		out[i] = string(status)
	}
	return out
}

func (s *service) showDrafts(w http.ResponseWriter, r *http.Request, status drafts.Status, code int, notice *tpl.Toast) {
	found, err := s.mail.Drafts(r.Context(), mailapp.DraftsInput{Status: status})
	if err != nil {
		s.failRead(w, r, err, "unknown draft")
		return
	}
	if code == http.StatusOK && web.FragmentNotModified(w, r, found.Revision) {
		return
	}
	views := make([]DraftView, len(found.Data.Cards))
	for i, c := range found.Data.Cards {
		views[i] = draftView(c.Row, c.Unexpected)
	}
	data := draftsPage{Drafts: views, Status: string(status), Statuses: statusNames(), Configured: reviewapp.AccountConfigured(r.Context())}
	s.page(w, r, code, "drafts", "drafts.gohtml", data, notice)
}

func (s *service) draftCard(w http.ResponseWriter, r *http.Request, id int64, code int, notice *tpl.Toast) {
	found, err := s.mail.Draft(r.Context(), mailapp.DraftInput{ID: id})
	if err != nil {
		s.failRead(w, r, err, "unknown draft")
		return
	}
	if code == http.StatusOK && r.Method == http.MethodGet && web.FragmentNotModified(w, r, found.Revision) {
		return
	}
	s.renderDraftCard(w, r, draftView(found.Data.Row, found.Data.Unexpected), code, notice)
}

func (s *service) renderDraftCard(w http.ResponseWriter, r *http.Request, view DraftView, code int, notice *tpl.Toast) {
	data := draftCardData{D: view, Configured: reviewapp.AccountConfigured(r.Context()), Partial: true}
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
	s.showDrafts(w, r, status, http.StatusOK, nil)
}

func (s *service) draftPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	found, err := s.mail.Draft(r.Context(), mailapp.DraftInput{ID: id})
	if err != nil {
		s.failRead(w, r, err, "unknown draft")
		return
	}
	if web.FragmentNotModified(w, r, found.Revision) {
		return
	}
	view := draftView(found.Data.Row, found.Data.Unexpected)
	configured := reviewapp.AccountConfigured(r.Context())
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
	changes := mailapp.DraftFields{Account: &account}
	to, _ := web.Value(form, "to")
	cc, _ := web.Value(form, "cc")
	subject, _ := web.Value(form, "subject")
	body, _ := web.Value(form, "body")
	changes.To = &mailapp.Recipients{Text: to}
	changes.Cc = &mailapp.Recipients{Text: cc}
	changes.Subject = &subject
	changes.Body = &body
	in := mailapp.DraftUpdateInput{ID: id, Fields: changes}
	if text, ok := web.Value(form, "revision"); ok {
		revision, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			web.Unprocessable(w, web.Missing("body", "revision"))
			return
		}
		in.Revision = &revision
	}
	_, err := s.mail.DraftUpdate(detached(r), in)
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	var stale drafts.Stale
	code := http.StatusUnprocessableEntity
	if errors.As(err, &stale) {
		code = http.StatusConflict
	}
	if errors.As(err, &refused) || errors.As(err, &disabled) || errors.As(err, &stale) {
		found, gerr := s.mail.Draft(detached(r), mailapp.DraftInput{ID: id, Submitted: &changes})
		row := &found.Data.Row
		if gerr == nil && row != nil {
			view := draftView(*row, found.Data.Unexpected)
			if errors.As(err, &stale) && in.Revision != nil {
				view.Revision = *in.Revision
			}
			if web.IsPartial(r) {
				s.renderDraftCard(w, r, view, code, toast("error", "Not saved", err.Error()))
			} else {
				s.page(w, r, code, "drafts", "drafts.gohtml",
					draftsPage{Drafts: []DraftView{view}, Status: view.Status, Statuses: statusNames(), Configured: reviewapp.AccountConfigured(r.Context())},
					toast("error", "Not saved", err.Error()))
			}
			return
		}
		s.reply(w, r, id, drafts.Draft, code, toast("error", "Not saved", err.Error()))
		return
	}
	if err != nil {
		web.Fail(w, r, err, func(status int) {
			s.reply(w, r, id, drafts.Draft, status, toast("error", "Not saved", application.Classify(err).Message))
		})
		return
	}
	s.reply(w, r, id, drafts.Draft, http.StatusOK, toast("success", "Saved", "Draft updated."))
}

func (s *service) draftDiscard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	_, err := s.mail.DraftDiscard(detached(r), mailapp.DraftInput{ID: id})
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not discarded", err.Error()))
		return
	}
	if err != nil {
		web.Fail(w, r, err, func(status int) {
			if application.Classify(err).Code == application.NotFound {
				status = http.StatusConflict
			}
			s.reply(w, r, id, drafts.Draft, status, toast("error", "Not discarded", application.Classify(err).Message))
		})
		return
	}
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
		var done application.Change[mailapp.DraftSendResult]
		done, err = s.mail.DraftSend(detached(r), mailapp.DraftSendInput{ID: id, Revision: rev})
		result = done.Data.Sent
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
	done, err := s.mail.DraftResolve(detached(r), mailapp.DraftResolveInput{ID: id, Outcome: outcome})
	if e, ok := errors.AsType[*application.Error](err); ok && e.Code == application.Invalid {
		s.fail(w, r, http.StatusUnprocessableEntity, e.Message)
		return
	}
	verdict := done.Data.Status
	var refused drafts.Error
	var disabled *module.OwnerDisabled
	if errors.As(err, &refused) || errors.As(err, &disabled) {
		s.reply(w, r, id, drafts.Unknown, http.StatusConflict, toast("error", "Not updated", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Unknown, http.StatusOK, toast("success", "Updated", "Draft marked "+string(verdict)+"."))
}
