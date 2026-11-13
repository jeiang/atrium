package reviewpages

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
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
	ctx := detached(r)
	known, err := s.live.Get(ctx, id)
	check(err)
	if known == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	var notice *tpl.Toast
	_, err = s.live.Undo(ctx, id)
	var refused live.UndoError
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &refused) || errors.As(err, &disabled):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not undone", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Undone")
	}
	found, err := s.live.Get(ctx, id)
	check(err)
	s.undone(w, r, reviewapp.MailEntry(*found), notice)
}

func (s *service) resolveMail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "action_id")
	if !ok {
		return
	}
	ctx := detached(r)
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	var notice *tpl.Toast
	_, err := s.live.Resolve(ctx, id, outcome, proposals.Resolver{Actor: "operator", Source: "ui"})
	var refused live.UndoError
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &refused) || errors.As(err, &disabled):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not settled", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = resolvedToast(outcome)
	}
	found, err := s.live.Get(ctx, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	s.undone(w, r, reviewapp.MailEntry(*found), notice)
}

func (s *service) undoEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	var missing *ledger.NotFound
	if _, err := s.ledger.Get(ctx, id); errors.As(err, &missing) {
		s.fail(w, r, http.StatusNotFound, "unknown effect")
		return
	} else if err != nil {
		panic(err)
	}
	var notice *tpl.Toast
	_, err := s.ledger.Undo(ctx, id, ledger.Resolver{Actor: "operator", Source: "ui"})
	var refused ledger.Refused
	var conflict *ledger.Conflict
	var settled *ledger.SettledMeanwhile
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &refused) || errors.As(err, &conflict) || errors.As(err, &settled) || errors.As(err, &disabled):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not undone", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = toast("success", "Undone")
	}
	found, err := s.ledger.Get(ctx, id)
	check(err)
	s.undone(w, r, reviewapp.EffectEntry(found), notice)
}

func (s *service) resolveEffect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "effect_id")
	if !ok {
		return
	}
	ctx := detached(r)
	outcome, _ := web.Value(web.FormValues(r), "outcome")
	var notice *tpl.Toast
	_, err := s.ledger.Resolve(ctx, id, ledger.Resolution{Outcome: outcome}, ledger.Resolver{Actor: "operator", Source: "ui"}, nil)
	var refused ledger.Refused
	var missing *ledger.NotFound
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &missing):
		s.fail(w, r, http.StatusNotFound, "unknown effect")
		return
	case errors.As(err, &refused) || errors.As(err, &disabled):
		if !web.IsPartial(r) {
			s.fail(w, r, http.StatusConflict, err.Error())
			return
		}
		notice = toast("error", "Not settled", err.Error())
	case err != nil:
		panic(err)
	default:
		notice = resolvedToast(outcome)
	}
	found, err := s.ledger.Get(ctx, id)
	check(err)
	s.undone(w, r, reviewapp.EffectEntry(found), notice)
}

func writeUndoDetail(err error) string {
	if strings.Contains(err.Error(), "unknown undo kind") {
		return "Not undone: atrium has no way to undo this change."
	}
	return err.Error()
}
