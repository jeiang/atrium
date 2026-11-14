package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/logx"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/urlx"
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
	parts, err := urlx.URLSplit(r.Header.Get("Referer"))
	return parts.Path, parts.Query, err == nil && parts.Netloc == r.Host
}

func backTo(r *http.Request) backLink {
	path, query, same := referer(r)
	if label, ok := backLabel(path); same && ok {
		return backLink{URL: withQuery(path, query), Label: label}
	}
	return backLink{URL: "/chat", Label: "Chat"}
}

func (s *service) loadMessage(w http.ResponseWriter, r *http.Request, id int64) *mailapp.MessageView {
	read, err := s.reads.Message(r.Context(), mailapp.MessageInput{ID: id})
	found := read.Data.View
	check(err)
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown message")
	}
	return found
}

func (s *service) correctContext(r *http.Request, found *mailapp.MessageView, context string, selected, problem *string) *correctForm {
	read, err := s.reads.Message(r.Context(), mailapp.MessageInput{ID: found.ID, Folders: true})
	folders := read.Data.Folders
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
	read, err := s.reads.Message(r.Context(), mailapp.MessageInput{ID: id, Draft: !web.IsPartial(r)})
	check(err)
	if read.Data.View == nil {
		s.fail(w, r, http.StatusNotFound, "unknown message")
		return
	}
	data := messageData{Message: read.Data.View, Back: backTo(r), FromDraft: read.Data.Draft}
	if web.IsPartial(r) {
		s.fragment(w, r, "_message.gohtml", data, nil)
		return
	}
	s.page(w, r, http.StatusOK, messagePage(data), "message.gohtml", data, nil)
}

func (s *service) correctForm(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "message_id")
	context := contextMessage
	if value, given := web.Value(r.URL.Query(), "context"); given {
		if context = value; context != contextRow && context != contextMessage {
			errs = append(errs, web.BadLiteral("query", "context", context, "'row'", "'message'"))
		}
	}
	if errs != nil {
		web.Unprocessable(w, errs...)
		return
	}
	read, err := s.reads.Message(r.Context(), mailapp.MessageInput{ID: id, Folders: true})
	check(err)
	found := read.Data.View
	if found == nil {
		s.fail(w, r, http.StatusNotFound, "unknown message")
		return
	}
	correct := &correctForm{MessageID: found.ID, Account: found.Account, Context: context, Folders: read.Data.Folders, Selected: found.CorrectedTo}
	s.fragment(w, r, "_correct.gohtml", correctData{Correct: correct}, nil)
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
	partial := web.IsPartial(r)
	mode := mailapp.Deferred
	if partial {
		mode = mailapp.Immediate
	}
	var problem *string
	done, err := s.reads.Correct(ctx, mailapp.CorrectInput{MessageID: id, Folder: dest, Mode: mode})
	var rejected mailapp.CorrectionError
	switch {
	case errors.As(err, &rejected):
		text := rejected.Error()
		problem = &text
	case err != nil:
		panic(err)
	}
	ok := problem == nil
	if ok && !partial {
		http.Redirect(w, r, "/messages/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	var notice *tpl.Toast
	if ok {
		notice = toast("success", "Correction recorded", fmt.Sprintf("Corrected to %s.%s", done.Data.Correction.Folder, done.Data.Note))
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
		if text := strings.TrimSpace(dest); text != "" {
			selected = &text
		}
		form_ = s.correctContext(r, fresh, shown, selected, problem)
	}
	switch {
	case context == contextRow && partial:
		row, err := s.reads.TriageRow(ctx, id)
		check(err)
		if row == nil {
			s.fail(w, r, http.StatusNotFound, "message has no decision")
			return
		}
		s.fragment(w, r, "_triage_row.gohtml", rowData{Row: row, Correct: form_}, notice)
	case partial:
		s.fragment(w, r, "_decision_card.gohtml", messageData{Message: fresh, Correct: form_}, notice)
	default:
		data := messageData{Message: fresh, Back: backTo(r), Correct: form_}
		s.page(w, r, http.StatusOK, messagePage(data), "message.gohtml", data, nil)
	}
}

func (s *service) togglePause(w http.ResponseWriter, r *http.Request, paused bool) {
	ctx := context.WithoutCancel(r.Context())
	var done application.Change[mailapp.PauseResult]
	var err error
	if paused {
		done, err = s.reads.Pause(ctx, mailapp.PauseInput{})
	} else {
		done, err = s.reads.Resume(ctx, mailapp.PauseInput{})
	}
	if !mailapp.Committed(err) {
		check(err)
	}
	if err := done.Data.NoticeError; err != nil {
		log.Warn("pause notice failed", logx.Err, err.Error())
	}
	target := "/triage"
	if path, query, same := referer(r); same {
		if safe := urlx.SafeReturn(withQuery(path, query)); safe != "/" {
			target = safe
		}
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
	notice := toast("success", "Undone", "")
	_, err := reviewapp.New(reviewapp.Deps{DB: s.deps.DB, Live: s.live}).Undo(ctx, reviewapp.UndoInput{Target: reviewapp.Target{Kind: reviewapp.MailTarget, ID: id}})
	var e *application.Error
	switch {
	case err == nil:
	case !errors.As(err, &e):
		panic(err)
	case e.Code == application.NotFound:
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	default:
		notice = toast("error", "Not undone", e.Message)
	}
	found, err := s.live.Get(ctx, id)
	check(err)
	row, err := s.reads.TriageRow(ctx, found.MessageID)
	check(err)
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "message has no decision")
		return
	}
	s.fragment(w, r, "_triage_row.gohtml", rowData{Row: row}, notice)
}

func messagePage(data messageData) string {
	if data.Back.Label == "Decisions" {
		return "triage"
	}
	return "chat"
}
