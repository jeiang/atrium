package reviewpages

import (
	"encoding/json"
	"net/http"
	"strings"

	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *service) activityPage(w http.ResponseWriter, r *http.Request) {
	category := web.QueryValue(r.URL.Query(), "category")
	offset := int64(0)
	if r.URL.Query().Has("offset") {
		raw := web.QueryValue(r.URL.Query(), "offset")
		n, ok := laxInt64(raw)
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "offset", raw))
			return
		}
		offset = n
	}
	found, err := s.review.Activity(r.Context(), reviewapp.ActivityInput{Category: category, Offset: int(max(offset, 0))})
	if err != nil {
		s.failRead(w, r, err, "")
		return
	}
	data := activityPage{Entries: found.Data.Entries, Category: category, Categories: reviewapp.Categories, NextOffset: found.Data.NextOffset, Offset: int(max(offset, 0))}
	if web.IsPartial(r) && offset > 0 {
		s.fragment(w, r, http.StatusOK, "_activity_rows.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "activity", "activity.gohtml", data, nil)
}

func (s *service) undone(w http.ResponseWriter, r *http.Request, entry reviewapp.ActivityEntry, notice *tpl.Toast) {
	if !web.IsPartial(r) {
		http.Redirect(w, r, "/activity", http.StatusSeeOther)
		return
	}
	if receiptView(r) {
		s.fragment(w, r, http.StatusOK, "_receipt.gohtml", activityRowData{E: entry}, notice)
		return
	}
	s.fragment(w, r, http.StatusOK, "_activity_row.gohtml", activityRowData{E: entry}, notice)
}

func (s *service) undoMail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "action_id")
	if !ok {
		return
	}
	_, err := s.review.Undo(detached(r), reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.MailTarget, ID: id}})
	s.mailDecided(w, r, id, err, "Not undone", toast("success", "Undone"))
}

func (s *service) mailDecided(w http.ResponseWriter, r *http.Request, id int64, err error, title string, success *tpl.Toast) {
	refused, answered := s.decided(w, r, err, "unknown action")
	if answered {
		return
	}
	notice := success
	if refused != nil {
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, refused.Error())
			return
		}
		notice = toast("error", title, refused.Error())
	}
	found, err := s.live.Get(detached(r), id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	s.undone(w, r, reviewapp.MailEntry(*found), notice)
}

func (s *service) resolveMail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "action_id")
	if !ok {
		return
	}
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	_, err := s.review.Resolve(detached(r), reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.MailTarget, ID: id}, Outcome: outcome})
	s.mailDecided(w, r, id, err, "Not settled", resolvedToast(outcome))
}

func (s *service) undoEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	_, err := s.review.Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}})
	s.effectDecided(w, r, id, err, "Not undone", toast("success", "Undone"))
}

func (s *service) effectDecided(w http.ResponseWriter, r *http.Request, id int64, err error, title string, success *tpl.Toast) {
	refused, answered := s.decided(w, r, err, "unknown effect")
	if answered {
		return
	}
	notice := success
	if refused != nil {
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, refused.Error())
			return
		}
		notice = toast("error", title, refused.Error())
	}
	found, err := s.ledger.Get(detached(r), id)
	check(err)
	s.undone(w, r, reviewapp.EffectEntry(found), notice)
}

func (s *service) resolveEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	_, err := s.review.Resolve(detached(r), reviewapp.ResolveInput{Target: reviewapp.Target{Kind: reviewapp.EffectTarget, ID: id}, Outcome: outcome})
	s.effectDecided(w, r, id, err, "Not settled", resolvedToast(outcome))
}

func writeUndoDetail(err error) string {
	if strings.Contains(err.Error(), "unknown undo kind") {
		return "Not undone: atrium has no way to undo this change."
	}
	return err.Error()
}
