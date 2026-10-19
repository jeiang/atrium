package reviewpages

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/smtp"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var statusFilters = []string{drafts.Draft, drafts.Unknown, drafts.Sent, drafts.Discarded}

const draftLimit = 100

func (s *service) draftViews(r *http.Request, rows []drafts.Row) []DraftView {
	out := make([]DraftView, len(rows))
	for i, row := range rows {
		unexpected, err := s.drafts.UnexpectedRecipients(r.Context(), row)
		check(err)
		out[i] = draftView(row, unexpected)
	}
	return out
}

func (s *service) showDrafts(w http.ResponseWriter, r *http.Request, status string, code int, notice *tpl.Toast) {
	rows, err := s.drafts.List(r.Context(), status, draftLimit)
	check(err)
	data := draftsPage{Drafts: s.draftViews(r, rows), Status: status, Statuses: statusFilters, Configured: accountConfigured(s.deps.Cfg)}
	s.page(w, r, code, "drafts", "drafts.gohtml", data, notice)
}

func (s *service) draftCard(w http.ResponseWriter, r *http.Request, id int64, code int, notice *tpl.Toast) {
	row, err := s.drafts.Get(r.Context(), id)
	check(err)
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "unknown draft")
		return
	}
	data := draftCardData{D: s.draftViews(r, []drafts.Row{*row})[0], Configured: accountConfigured(s.deps.Cfg), Partial: true}
	s.fragment(w, r, code, "_draft.gohtml", data, notice)
}

// reply answers a draft action: the card for htmx, the drafts page of status for a plain post.
func (s *service) reply(w http.ResponseWriter, r *http.Request, id int64, status string, code int, notice *tpl.Toast) {
	if web.IsPartial(r) {
		s.draftCard(w, r, id, code, notice)
		return
	}
	s.showDrafts(w, r, status, code, notice)
}

func (s *service) draftsPage(w http.ResponseWriter, r *http.Request) {
	status := drafts.Draft
	if r.URL.Query().Has("status") {
		status = queryLast(r, "status")
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
	configured := accountConfigured(s.deps.Cfg)
	if web.IsPartial(r) {
		s.fragment(w, r, http.StatusOK, "_draft_preview.gohtml", draftCardData{D: view, Configured: configured}, nil)
		return
	}
	s.page(w, r, http.StatusOK, "drafts", "drafts.gohtml",
		draftsPage{Drafts: []DraftView{view}, Status: view.Status, Statuses: statusFilters, Configured: configured}, nil)
}

func (s *service) draftUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	form := formValues(r)
	account, found := formLast(form, "account")
	if !found {
		web.Unprocessable(w, web.Missing("body", "account"))
		return
	}
	changes := map[string]any{"account": account}
	for _, name := range []string{"to", "cc", "subject", "body"} {
		value, _ := formLast(form, name)
		changes[name] = value
	}
	_, err := s.drafts.Update(detached(r), id, changes)
	var refused drafts.Error
	if errors.As(err, &refused) {
		s.reply(w, r, id, drafts.Draft, http.StatusUnprocessableEntity, toast("error", "Not saved", err.Error()))
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
	if errors.As(err, &refused) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not discarded", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Draft, http.StatusOK, toast("success", "Discarded", "Draft discarded."))
}

// reviewedAt is float(updated_at); ok is false where Python's float() raises ValueError. Underscores are accepted
// between digits only.
func reviewedAt(text string) (float64, bool) {
	text = py.Strip(text)
	if strings.ContainsAny(text, "xXpP") {
		return 0, false
	}
	if strings.Contains(text, "_") {
		var b strings.Builder
		for i := range len(text) {
			switch {
			case text[i] != '_':
				b.WriteByte(text[i])
			case i == 0 || i == len(text)-1 || !isDigit(text[i-1]) || !isDigit(text[i+1]):
				return 0, false
			}
		}
		text = b.String()
	}
	if text == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && numErr.Err == strconv.ErrRange {
			return v, true
		}
		return 0, false
	}
	return v, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (s *service) draftSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "draft_id")
	if !ok {
		return
	}
	updated, _ := formLast(formValues(r), "updated_at")
	var result drafts.Result
	var err error
	if at, valid := reviewedAt(updated); valid {
		result, err = s.drafts.Send(detached(r), id, at)
	} else {
		err = drafts.Error("draft changed, review again")
	}
	var refused drafts.Error
	var failed smtp.SendError
	if errors.As(err, &refused) || errors.As(err, &failed) {
		s.reply(w, r, id, drafts.Draft, http.StatusConflict, toast("error", "Not sent", err.Error()))
		return
	}
	check(err)
	detail := result.Filed
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
	outcome, _ := formLast(formValues(r), "outcome")
	var verdict string
	switch outcome {
	case "sent":
		verdict = drafts.Sent
	case "draft":
		verdict = drafts.Draft
	default:
		s.fail(w, r, http.StatusUnprocessableEntity, "outcome must be sent or draft")
		return
	}
	err := s.drafts.ResolveUnknown(detached(r), id, verdict)
	var refused drafts.Error
	if errors.As(err, &refused) {
		s.reply(w, r, id, drafts.Unknown, http.StatusConflict, toast("error", "Not updated", err.Error()))
		return
	}
	check(err)
	s.reply(w, r, id, drafts.Unknown, http.StatusOK, toast("success", "Updated", "Draft marked "+verdict+"."))
}
