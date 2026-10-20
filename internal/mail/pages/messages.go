package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	contextRow     = "row"
	contextMessage = "message"
)

var log = logx.Logger("atrium.web")

func backLabel(path string) (string, bool) {
	switch path {
	case "/chat":
		return "Chat", true
	case "/triage":
		return "Decisions", true
	}
	return "", false
}

func withQuery(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

func referer(r *http.Request) (path, query string, sameHost bool) {
	parts, err := py.URLSplit(r.Header.Get("Referer"))
	return parts.Path, parts.Query, err == nil && parts.Netloc == r.Host
}

func backTo(r *http.Request) backLink {
	path, query, same := referer(r)
	if label, ok := backLabel(path); same && ok {
		return backLink{URL: withQuery(path, query), Label: label}
	}
	return backLink{URL: "/chat", Label: "Chat"}
}

func (s *service) loadMessage(w http.ResponseWriter, r *http.Request, id int64) *message.View {
	found, err := message.Get(r.Context(), s.deps.DB.Read, s.deps.Cfg.CacheDir, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown message")
	}
	return found
}

func (s *service) correctContext(r *http.Request, found *message.View, context string, selected, problem *string) *correctForm {
	folders, err := triage.Folders(r.Context(), s.deps.DB.Read, found.Account)
	check(err)
	return &correctForm{
		MessageID: found.ID, Account: found.Account, Context: context, Folders: folders, Selected: selected, Error: problem,
	}
}

func (s *service) messagePage(w http.ResponseWriter, r *http.Request) {
	id, bad := pathInt(r, "message_id")
	if bad != nil {
		web.Unprocessable(w, bad...)
		return
	}
	found := s.loadMessage(w, r, id)
	if found == nil {
		return
	}
	draft, err := s.drafts.ForMessage(r.Context(), id)
	check(err)
	data := messageData{Message: found, Back: backTo(r), FromDraft: draft}
	if web.IsPartial(r) {
		s.fragment(w, r, "_message.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "message.gohtml", data, nil)
}

func (s *service) correctForm(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "message_id")
	context := contextMessage
	if values, given := r.URL.Query()["context"]; given {
		if context = web.LastValue(values, ""); context != contextRow && context != contextMessage {
			errs = append(errs, web.BadLiteral("query", "context", context, "'row'", "'message'"))
		}
	}
	if errs != nil {
		web.Unprocessable(w, errs...)
		return
	}
	found := s.loadMessage(w, r, id)
	if found == nil {
		return
	}
	s.fragment(w, r, "_correct.gohtml", correctData{Correct: s.correctContext(r, found, context, found.CorrectedTo, nil)}, nil)
}

func (s *service) correctSubmit(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "message_id")
	form := web.FormValues(r)
	dest := web.FormString(form, "dest", "")
	context := web.FormString(form, "context", contextMessage)
	if context != contextRow && context != contextMessage {
		errs = append(errs, web.BadLiteral("body", "context", context, "'row'", "'message'"))
	}
	if errs != nil {
		web.Unprocessable(w, errs...)
		return
	}
	ctx := r.Context()
	found := s.loadMessage(w, r, id)
	if found == nil {
		return
	}
	var problem *string
	recorded, err := corrections.Record(ctx, s.deps.DB, id, dest, corrections.Source, s.now())
	var rejected corrections.Error
	switch {
	case errors.As(err, &rejected):
		text := rejected.Error()
		problem = &text
	case err != nil:
		panic(err)
	}
	ok := problem == nil
	partial := web.IsPartial(r)
	if ok && !partial {
		http.Redirect(w, r, "/messages/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	var notice *tpl.Toast
	if ok {
		notice = toast("success", "Correction recorded", fmt.Sprintf("Corrected to %s.%s", recorded.Folder, s.moveCorrection(r, found, id)))
	}
	fresh := s.loadMessage(w, r, id)
	if fresh == nil {
		return
	}
	shown := contextMessage
	if partial {
		shown = context
	}
	var form_ *correctForm
	if !ok {
		var selected *string
		if text := py.Strip(dest); text != "" {
			selected = &text
		}
		form_ = s.correctContext(r, fresh, shown, selected, problem)
	}
	switch {
	case context == contextRow && partial:
		row, err := triage.Get(ctx, s.deps.DB.Read, id)
		check(err)
		if row == nil {
			s.fail(w, r, http.StatusNotFound, "message has no decision")
			return
		}
		s.fragment(w, r, "_triage_row.gohtml", rowData{Row: row, Correct: form_}, notice)
	case partial:
		s.fragment(w, r, "_decision_card.gohtml", messageData{Message: fresh, Correct: form_}, notice)
	default:
		s.page(w, r, http.StatusOK, "message.gohtml", messageData{Message: fresh, Back: backTo(r), Correct: form_}, nil)
	}
}

func (s *service) moveCorrection(r *http.Request, found *message.View, id int64) string {
	if s.deps.Cfg.Mode != safety.Live {
		return ""
	}
	ctx := context.WithoutCancel(r.Context())
	out, err := func() (out appliedOutcome, err error) {
		lock, err := db.Exclusive(s.deps.Cfg.DBPath)
		if err != nil {
			return out, err
		}
		defer func() { _ = lock.Close() }()
		done, err := s.applier.ApplyCorrections(ctx, found.Account, []int64{id})
		return appliedOutcome{done.Ran, done.Written, done.Failed + done.Skipped}, err
	}()
	if err != nil {
		log.Error(fmt.Sprintf("moving corrected message %d failed", id), logx.Err, err.Error())
		return " It could not be moved yet: " + py.ExceptionClass(err) + "."
	}
	switch {
	case !out.ran:
		return " Moving is paused; it will move after you resume."
	case out.written > 0:
		return " The message was moved."
	case out.unmoved > 0:
		return " It could not be moved; see Decisions."
	}
	return ""
}

type appliedOutcome struct {
	ran     bool
	written int
	unmoved int
}

func (s *service) togglePause(w http.ResponseWriter, r *http.Request, paused bool) {
	check(s.live.SetPaused(paused))
	target := "/triage"
	if path, query, same := referer(r); same && strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
		target = withQuery(path, query)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *service) liveUndo(w http.ResponseWriter, r *http.Request) {
	id, bad := pathInt(r, "action_id")
	if bad != nil {
		web.Unprocessable(w, bad...)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	found, err := live.Get(ctx, s.deps.DB.Read, id)
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	notice := toast("success", "Undone", "")
	var refused live.UndoError
	switch _, err := s.live.Undo(ctx, id); {
	case errors.As(err, &refused):
		notice = toast("error", "Not undone", refused.Error())
	case err != nil:
		panic(err)
	}
	row, err := triage.Get(ctx, s.deps.DB.Read, found.MessageID)
	check(err)
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "message has no decision")
		return
	}
	s.fragment(w, r, "_triage_row.gohtml", rowData{Row: row}, notice)
}
