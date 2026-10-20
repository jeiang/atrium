package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/mail/search"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

type listData struct {
	Threads   []threads.Listed `json:"threads"`
	Q         string           `json:"q"`
	View      string           `json:"view"`
	ProjectID int64            `json:"project_id"`
}

func (p *pages) listing(w http.ResponseWriter, r *http.Request, q, view string, projectID int64) {
	match := ""
	if q != "" {
		var err error
		match, err = search.FTSQuery(q)
		check(err)
	}
	found, err := threads.ListThreads(r.Context(), p.deps.DB.Read, threads.ListOpts{Match: match, Limit: threadLimit, Archived: view == archived, ProjectID: projectID})
	check(err)
	if found == nil {
		found = []threads.Listed{}
	}
	data := listData{Threads: found, Q: q, View: view, ProjectID: projectID}
	if web.IsPartial(r) {
		p.fragment(w, r, http.StatusOK, "_thread_list.gohtml", data)
		return
	}
	p.page(w, r, "chat.gohtml", data)
}

func (p *pages) chatIndex(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := web.QueryValue(query, "view")
	if view != "" && view != archived {
		p.fail(w, r, http.StatusUnprocessableEntity, "unknown view")
		return
	}
	p.listing(w, r, py.Strip(web.QueryValue(query, "q")), view, 0)
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
	blocks, err := files.IngestUploads(detached(r), p.deps.DB.Write, p.deps.Cfg.FilesDir(), uploads, p.now(), files.Owner{ThreadID: thread.ID}, &limit)
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
	text := py.Strip(web.FormString(form, "text", ""))
	if len(uploads) > 0 && text == "" {
		p.failure(w, r, "Nothing sent", "Type a message to go with the files.")
		return
	}
	ctx := detached(r)
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
	threadID, err := service.CreateThread(ctx, p.deps.DB.Write, p.now(), threads.NewThread{Kind: threads.Chat, ProjectID: project})
	check(err)
	thread, err := threads.Get(ctx, p.deps.DB.Read, threadID)
	check(err)
	attached, ok := p.attach(w, r, thread, uploads)
	if !ok {
		check(p.svc.DeleteThread(ctx, threadID))
		return
	}
	if text != "" {
		_, err := p.svc.StartRun(ctx, threadID, text, service.Start{Kind: threads.Chat, Operator: true, Files: attached})
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

func (p *pages) setFlag(w http.ResponseWriter, r *http.Request, column string) {
	threadID, errs := pathInt(r, "thread_id")
	form := web.FormValues(r)
	value := formInt(form, "body", column, 1, &errs)
	projectID := formInt(form, "body", "project_id", 0, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	ok, err := threads.SetFlag(detached(r), p.deps.DB.Write, threadID, column, value != 0)
	check(err)
	if !ok {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return
	}
	if web.IsPartial(r) {
		view := web.FormString(form, "view", "")
		if view != archived {
			view = ""
		}
		p.listing(w, r, py.Strip(web.FormString(form, "q", "")), view, projectID)
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
	changed, err := threads.SetOptions(detached(r), p.deps.DB.Write, threadID, web.FormString(form, "effort", ""), web.FormString(form, "context_limit", ""))
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
	p.fragment(w, r, http.StatusOK, "_meter.gohtml", map[string]any{"view": view})
}

func (p *pages) chatDelete(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	if p.refused(w, r, p.svc.DeleteThread(detached(r), threadID)) {
		return
	}
	if web.IsPartial(r) {
		hxRedirect(w, "/chat")
		return
	}
	redirect(w, r, "/chat")
}
