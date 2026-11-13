package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	p.page(w, r, "chat.gohtml", read.Data)
}

func (p *pages) threadList(w http.ResponseWriter, r *http.Request, q, view string) {
	read, err := p.assistant().Home(r.Context(), asstapp.HomeInput{Q: q, View: view, Limit: config.TuningChatThreadLimit, List: true})
	if err != nil {
		p.readFailed(w, r, err)
		return
	}
	p.fragment(w, r, http.StatusOK, "_thread_list.gohtml", read.Data)
}

func attachmentBlocks(blocks []files.AttachmentBlock) []json.RawMessage {
	out := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		raw, err := json.Marshal(b)
		check(err)
		out[i] = raw
	}
	return out
}

func (p *pages) attach(w http.ResponseWriter, r *http.Request, thread *threads.Thread, uploads []files.UploadedFile) ([]json.RawMessage, bool) {
	if len(uploads) == 0 {
		return nil, true
	}
	limit := threads.ContextLimitOf(*thread)
	blocks, err := files.IngestUploads(detached(r), p.deps.DB, p.deps.Cfg.FilesDir(), uploads, p.now(), files.ThreadOwner(thread.ID), &limit)
	var refused files.FileError
	if errors.As(err, &refused) {
		p.failure(w, r, "Not attached", err.Error())
		return nil, false
	}
	check(err)
	return attachmentBlocks(blocks), true
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
	ctx := detached(r)
	refs, err := references.Resolve(ctx, p.deps.DB.Read, 0, text)
	if p.refused(w, r, err) {
		return
	}
	about, err := p.assistant().About(ctx, asstapp.AboutInput{Ref: web.FormString(form, "about", "")})
	check(err)
	if about := about.Data; about != nil && text != "" {
		text = about.Line + "\n\n" + text
		if about.Project != 0 {
			projectID = about.Project
		}
	}
	var project *int64
	if projectID != 0 {
		found, err := projects.Get(ctx, p.deps.DB.Read, projectID)
		check(err)
		if found == nil || found.ID == 0 {
			p.fail(w, r, http.StatusNotFound, "unknown project")
			return
		}
		if problem, archived := archivedProject(found, "start chats in it"); archived {
			p.failure(w, r, "Project archived", problem)
			return
		}
		project = &projectID
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
	threadID, err := threads.Create(ctx, p.deps.DB.Write, p.now(), threads.Spec{
		Kind: threads.Chat, ProjectID: project,
		Model: web.FormString(form, "model", ""), Effort: web.FormString(form, "effort", ""), ContextLimit: optional(form, "context_limit"),
		Thinking: web.FormString(form, "thinking", ""), WebSearch: &webSearch, CostCapUSD: costCap,
	})
	var refused threads.OptionError
	if errors.As(err, &refused) {
		p.failure(w, r, "Not started", err.Error())
		return
	}
	check(err)
	thread, err := threads.Get(ctx, p.deps.DB.Read, threadID)
	check(err)
	attached, ok := p.attach(w, r, thread, uploads)
	if !ok {
		check(p.svc().DeleteThread(ctx, threadID))
		return
	}
	if text != "" {
		_, err := p.svc().StartRun(ctx, threadID, text, service.Start{Kind: threads.Chat, Operator: true, Files: append(attached, refs...)})
		if p.refused(w, r, err) {
			return
		}
	}
	to := "/chat/" + strconv.FormatInt(threadID, 10)
	if web.IsPartial(r) {
		hxRedirect(w, to)
		return
	}
	redirect(w, r, to)
}

func (p *pages) setFlag(w http.ResponseWriter, r *http.Request, column string, set func(context.Context, db.Querier, int64, bool) (bool, error)) {
	threadID, errs := pathInt(r, "thread_id")
	form := web.FormValues(r)
	value := formInt(form, "body", column, 1, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	ok, err := set(detached(r), p.deps.DB.Write, threadID, value != 0)
	check(err)
	if !ok {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
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
	var target *int64
	if projectID != 0 {
		found, err := projects.Get(detached(r), p.deps.DB.Read, projectID)
		check(err)
		if found == nil {
			p.fail(w, r, http.StatusNotFound, "unknown project")
			return
		}
		if problem, archived := archivedProject(found, "move chats into it"); archived {
			p.failure(w, r, "Project archived", problem)
			return
		}
		target = &projectID
	}
	moved, err := projects.MoveThread(detached(r), p.deps.DB.Write, threadID, target)
	var refused projects.ProjectError
	if errors.As(err, &refused) {
		p.fail(w, r, http.StatusNotFound, err.Error())
		return
	}
	check(err)
	if !moved {
		p.fail(w, r, http.StatusNotFound, "unknown chat thread")
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
	changed, err := threads.SetOptions(detached(r), p.deps.DB.Write, threadID, web.FormString(form, "model", ""), web.FormString(form, "effort", ""), web.FormString(form, "context_limit", ""))
	var refused threads.OptionError
	if errors.As(err, &refused) {
		p.failure(w, r, "Not saved", err.Error())
		return
	}
	check(err)
	if !changed {
		p.fail(w, r, http.StatusNotFound, "unknown chat thread")
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
	if p.refused(w, r, p.svc().DeleteThread(detached(r), threadID)) {
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
