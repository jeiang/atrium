package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func (p *pages) loadThread(w http.ResponseWriter, r *http.Request, threadID int64) (map[string]any, bool) {
	v, err := service.View(r.Context(), p.deps.DB.Read, threadID)
	check(err)
	if v == nil {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return nil, false
	}
	view, err := threadViewDict(v)
	check(err)
	return view, true
}

func (p *pages) loadRun(w http.ResponseWriter, r *http.Request, threadID, runID int64) (*journal.Run, bool) {
	found, err := journal.GetRun(r.Context(), p.deps.DB.Read, runID)
	check(err)
	if found == nil || found.ThreadID != threadID {
		p.fail(w, r, http.StatusNotFound, "unknown run")
		return nil, false
	}
	return found, true
}

func threadOf(view map[string]any) threads.Thread { return view["thread"].(threads.Thread) }

func itemsOf(view map[string]any) []map[string]any { return view["items"].([]map[string]any) }

func seqOf(item map[string]any) int64 { return item["seq"].(int64) }

func runOf(item map[string]any) int64 {
	if id, ok := item["run_id"].(*int64); ok && id != nil {
		return *id
	}
	return 0
}

func (p *pages) threadContext(w http.ResponseWriter, r *http.Request, threadID int64) (map[string]any, bool) {
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return nil, false
	}
	t := threadOf(view)
	return map[string]any{
		"view": view, "thread_id": threadID,
		"composer": composerData(t.Model, t.Effort, threads.ContextLimitOf(t), threadID),
	}, true
}

func (p *pages) transcript(w http.ResponseWriter, r *http.Request, threadID int64, reswap string) {
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	if reswap != "" {
		w.Header().Set("HX-Reswap", reswap)
	}
	p.fragment(w, r, http.StatusOK, "_transcript.gohtml", data)
}

func (p *pages) chatThread(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	ctx := r.Context()
	work, err := workView(ctx, p.deps.DB.Read, threadID, p.now())
	check(err)
	list, err := projects.List(ctx, p.deps.DB.Read, false)
	check(err)
	if list == nil {
		list = []projects.Project{}
	}
	data["work"], data["projects"], data["project"] = work, list, nil
	if t := threadOf(data["view"].(map[string]any)); t.ProjectID != nil && *t.ProjectID != 0 {
		project, err := projects.Get(ctx, p.deps.DB.Read, *t.ProjectID)
		check(err)
		data["project"] = project
	}
	if web.IsPartial(r) {
		p.fragment(w, r, http.StatusOK, "_thread.gohtml", data)
		return
	}
	p.page(w, r, "thread.gohtml", data)
}

func (p *pages) chatMessage(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	form, uploads, ok := p.readUploads(w, r)
	if !ok {
		return
	}
	text := py.Strip(web.FormString(form, "text", ""))
	if text == "" {
		p.failure(w, r, "Nothing sent", emptyMessage)
		return
	}
	ctx := detached(r)
	thread, err := threads.Get(ctx, p.deps.DB.Read, threadID)
	check(err)
	if thread == nil {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return
	}
	if thread.Kind == threads.Subagent {
		p.fail(w, r, http.StatusConflict, "subagent threads are read-only")
		return
	}
	attached, ok := p.attach(w, r, thread, uploads)
	if !ok {
		return
	}
	back := "/chat/" + strconv.FormatInt(threadID, 10)
	if _, edit := web.FormField(form, "edit"); edit {
		_, err := p.svc.EditLast(ctx, threadID, text, attached)
		if p.refused(w, r, err) {
			return
		}
		if !web.IsPartial(r) {
			redirect(w, r, back)
			return
		}
		p.transcript(w, r, threadID, "innerHTML")
		return
	}
	sent, err := p.svc.Send(ctx, threadID, text, attached)
	if p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, back)
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	if sent.Queued {
		queued := []map[string]any{}
		for _, q := range view["queued"].([]map[string]any) {
			if q["id"] == sent.ID {
				queued = append(queued, q)
			}
		}
		p.fragment(w, r, http.StatusOK, "_queued.gohtml", map[string]any{"view": view, "queued": queued})
		return
	}
	items := []map[string]any{}
	var after int64
	for _, item := range itemsOf(view) {
		if runOf(item) == sent.ID {
			items = append(items, item)
			after = max(after, seqOf(item))
		}
	}
	p.fragment(w, r, http.StatusOK, "_run_started.gohtml", map[string]any{
		"view": view, "thread_id": threadID, "run_id": sent.ID, "items": items, "after": after,
	})
}

func (p *pages) chatRegenerate(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	_, err := p.svc.Regenerate(detached(r), threadID)
	if p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	p.transcript(w, r, threadID, "")
}

func (p *pages) chatStop(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	ctx := detached(r)
	active, err := journal.ActiveRun(ctx, p.deps.DB.Read, threadID)
	check(err)
	stopped := false
	if active != nil {
		stopped, err = journal.RequestStop(ctx, p.deps.DB.Write, active.ID)
		check(err)
	}
	if !stopped {
		p.fail(w, r, http.StatusConflict, "no run is running")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *pages) chatBranch(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	form := web.FormValues(r)
	raw, found := web.FormField(form, "seq")
	var seq int64
	if !found {
		errs = append(errs, web.Missing("body", "seq"))
	} else if n, ok := py.LaxInt64(raw); ok {
		seq = n
	} else {
		errs = append(errs, web.BadInt("body", "seq", raw))
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	parent, err := threads.Get(detached(r), p.deps.DB.Read, threadID)
	check(err)
	if parent != nil && parent.ProjectID != nil && *parent.ProjectID != 0 {
		project, err := projects.Get(detached(r), p.deps.DB.Read, *parent.ProjectID)
		check(err)
		if project != nil {
			if problem, archived := archivedProject(project, "start chats in it"); archived {
				p.failure(w, r, "Project archived", problem)
				return
			}
		}
	}
	child, err := p.svc.Branch(detached(r), threadID, seq)
	var unknown service.UnknownThread
	var redo service.NothingToRedo
	switch {
	case errors.As(err, &unknown):
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return
	case errors.As(err, &redo):
		p.fail(w, r, http.StatusNotFound, err.Error())
		return
	}
	check(err)
	redirect(w, r, "/chat/"+strconv.FormatInt(child, 10))
}

func (p *pages) chatWork(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	work, err := workView(r.Context(), p.deps.DB.Read, threadID, p.now())
	check(err)
	data["work"] = work
	p.fragment(w, r, http.StatusOK, "_work_update.gohtml", data)
}

func queryAfter(r *http.Request, errs *[]web.FieldError) int64 {
	raw := r.URL.Query()["after"]
	if len(raw) == 0 {
		return 0
	}
	value := raw[len(raw)-1]
	n, ok := py.LaxInt64(value)
	if !ok {
		*errs = append(*errs, web.BadInt("query", "after", value))
	}
	return n
}

func (p *pages) chatRun(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	runID, more := pathInt(r, "run_id")
	errs = append(errs, more...)
	after := queryAfter(r, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	found, ok := p.loadRun(w, r, threadID, runID)
	if !ok {
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	if found.State == journal.Running {
		p.fragment(w, r, http.StatusOK, "_run_live.gohtml", map[string]any{"view": view, "thread_id": threadID, "run_id": runID, "after": after})
		return
	}
	items := []map[string]any{}
	for _, item := range itemsOf(view) {
		if seqOf(item) > after && runOf(item) == runID {
			items = append(items, item)
		}
	}
	p.fragment(w, r, http.StatusOK, "_run_done.gohtml", map[string]any{"view": view, "thread_id": threadID, "run": found, "items": items})
}

func (p *pages) chatResume(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	runID, more := pathInt(r, "run_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, ok := p.loadRun(w, r, threadID, runID); !ok {
		return
	}
	err := p.svc.StartResume(detached(r), runID)
	if errors.As(err, new(service.NotInterrupted)) {
		p.fail(w, r, http.StatusConflict, "run is not interrupted")
		return
	}
	check(err)
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	var last int64
	for _, item := range itemsOf(view) {
		last = max(last, seqOf(item))
	}
	p.fragment(w, r, http.StatusOK, "_run_live.gohtml", map[string]any{"view": view, "thread_id": threadID, "run_id": runID, "after": last})
}
