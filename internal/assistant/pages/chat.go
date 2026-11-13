package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web"
)

func (p *pages) chatIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	partial := web.IsPartial(r)
	read, err := p.assistant().Home(r.Context(), asstapp.HomeInput{Q: web.QueryValue(query, "q"), View: web.QueryValue(query, "view"),
		About: web.QueryValue(query, "about"), Limit: config.TuningChatThreadLimit, List: partial})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	if partial {
		if web.FragmentNotModified(w, r, read.Revision) {
			return
		}
		p.fragment(w, r, http.StatusOK, "_thread_list.gohtml", read.Data)
		return
	}
	data := HomePage{Home: read.Data}
	if read.Data.View == "" && read.Data.Q == "" {
		data.Needs, data.Receipts, err = p.home(r.Context())
		check(err)
		if data.Agenda {
			day, err := p.assistant().Home(r.Context(), asstapp.HomeInput{Agenda: true})
			if err != nil {
				data.AgendaError = application.Classify(err).Error()
			} else {
				data.Day = day.Data.Day
			}
		}
	}
	p.page(w, r, "chat.gohtml", data)
}

func (p *pages) threadList(w http.ResponseWriter, r *http.Request, q, view string) {
	read, err := p.assistant().Home(r.Context(), asstapp.HomeInput{Q: q, View: view, Limit: config.TuningChatThreadLimit, List: true})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	p.fragment(w, r, http.StatusOK, "_thread_list.gohtml", read.Data)
}

func (p *pages) chatNew(w http.ResponseWriter, r *http.Request) {
	form, uploads, ok := p.readUploads(w, r)
	if !ok {
		return
	}
	var errs []web.FieldError
	projectID := formInt(form, "body", "project_id", 0, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	text := strings.TrimSpace(web.FormString(form, "text", ""))
	if len(uploads) > 0 && text == "" {
		p.failure(w, r, "Nothing sent", "Type a message to go with the files.")
		return
	}
	webSearch := true
	if values, ok := form["web_search"]; ok {
		webSearch = slices.Contains(values, "1")
	}
	var costCap *float64
	if raw := web.FormString(form, "cost_cap", ""); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			costCap = &value
		}
	}
	started, err := p.assistant().ThreadStart(detached(r), asstapp.StartInput{
		Kind: threads.Chat, ProjectID: projectID, About: web.FormString(form, "about", ""),
		Model: web.FormString(form, "model", ""), Effort: web.FormString(form, "effort", ""), ContextLimit: optional(form, "context_limit"),
		Thinking: web.FormString(form, "thinking", ""), WebSearch: &webSearch, CostCapUSD: costCap,
		Text: text, Files: uploads, Operator: true,
	})
	if p.refused(w, r, err) {
		return
	}
	to := "/chat/" + strconv.FormatInt(started.Data.ThreadID, 10)
	if web.IsPartial(r) {
		hxRedirect(w, to)
		return
	}
	redirect(w, r, to)
}

func (p *pages) setFlag(w http.ResponseWriter, r *http.Request, column string, set func(context.Context, asstapp.FlagInput) (application.Change[asstapp.ThreadChanged], error)) {
	threadID, errs := pathInt(r, "thread_id")
	form := web.FormValues(r)
	value := formInt(form, "body", column, 1, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, err := set(detached(r), asstapp.FlagInput{ThreadID: threadID, Set: value != 0}); p.refused(w, r, err) {
		return
	}
	if web.IsPartial(r) {
		view := web.FormString(form, "view", "")
		if view != archived {
			view = asstapp.AllThreads
		}
		p.threadList(w, r, web.FormString(form, "q", ""), view)
		return
	}
	back := "/chat/" + strconv.FormatInt(threadID, 10)
	if column == "archived" && value != 0 {
		back = "/chat"
	}
	redirect(w, r, back)
}

func (p *pages) chatProject(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	projectID := formInt(web.FormValues(r), "body", "project_id", 0, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, err := p.assistant().ThreadProject(detached(r), asstapp.MoveInput{ThreadID: threadID, ProjectID: projectID}); p.refused(w, r, err) {
		return
	}
	redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
}

func (p *pages) chatOptions(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	form := web.FormValues(r)
	_, err := p.assistant().ThreadOptions(detached(r), asstapp.OptionsInput{ThreadID: threadID, Model: web.FormString(form, "model", ""),
		Effort: web.FormString(form, "effort", ""), ContextLimit: web.FormString(form, "context_limit", "")})
	var refused threads.OptionError
	if errors.As(err, &refused) {
		p.failure(w, r, "Not saved", err.Error())
		return
	}
	if p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	thread := threadOf(view)
	p.fragment(w, r, http.StatusOK, "_options_saved.gohtml", OptionsSaved{
		View: view, Composer: composerOf(r.Context(), thread, threadID),
	})
}

func (p *pages) chatDelete(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	if _, err := p.assistant().ThreadDelete(detached(r), asstapp.ThreadInput{ID: threadID}); p.refused(w, r, err) {
		return
	}
	if web.IsPartial(r) {
		hxRedirect(w, "/chat")
		return
	}
	redirect(w, r, "/chat")
}

func optional(form url.Values, key string) any {
	if v := web.FormString(form, key, ""); v != "" {
		return v
	}
	return nil
}
